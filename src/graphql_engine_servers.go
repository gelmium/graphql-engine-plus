package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
)

func StartScannerForBuffer(ctx context.Context, stdoutReader io.Reader) chan string {
	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(stdoutReader)
		for scanner.Scan() {
			lines <- scanner.Text()
			// check for ctx cancel
			if ctx.Err() != nil {
				break
			}
		}
	}()
	return lines
}

func ParseLog(ctx context.Context, lines chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case line := <-lines:
			// check if line contain this error
			if strings.Contains(line, "cannot set transaction read-write mode during recovery") {
				// ignore this line
				continue
			}
			fmt.Println(line)
		}
	}
}

func StartGraphqlEngineServers(
	ctx context.Context, mainCtxCancelFn context.CancelFunc,
	startupCtx context.Context, startupDoneFn context.CancelFunc,
	startupReadonlyCtx context.Context, startupReadonlyDoneFn context.CancelFunc,
	shutdownErrorChanel chan error) {
	// create an empty list of cmds
	var cmds []*exec.Cmd
	// create a waitgroup to wait for all cmds to finish
	var wg sync.WaitGroup
	// start scripting-server at port 8880
	log.Info("Starting scripting-server at unix /tmp/scripting.sock and tcp 8880")
	cmd0 := exec.CommandContext(ctx, "python3", "/graphql-engine/scripting/server.py", "--path", "/tmp/scripting.sock", "--port", "8880")
	// route the output to stdout
	cmd0.Stderr = os.Stderr
	cmd0.Stdout = os.Stdout
	// override the cancel function to send sigterm instead of kill
	cmd0.Cancel = func() error {
		return cmd0.Process.Signal(syscall.SIGTERM)
	}
	if err := cmd0.Start(); err != nil {
		log.Error("Failed to start scripting-server:", err)
		os.Exit(1)
	}
	// call Wait() in a goroutine to avoid blocking the main thread
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmd0.Wait()
	}()
	cmds = append(cmds, cmd0)
	log.Info("Starting graphql-engine primary at port 8881")
	// start graphql-engine schema v1
	cmd1 := exec.CommandContext(ctx, "graphql-engine", "serve", "--server-port", "8881")
	// route the output to stdout
	cmd1.Stderr = os.Stderr
	cmd1.Stdout = os.Stdout
	// override the cancel function to send sigterm instead of kill
	cmd1.Cancel = func() error {
		return cmd1.Process.Signal(syscall.SIGTERM)
	}
	if err := cmd1.Start(); err != nil {
		log.Error("Failed to start primary-engine:", err)
		os.Exit(1)
	}
	// call Wait() in a goroutine to avoid blocking the main thread
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmd1.Wait()
	}()
	cmds = append(cmds, cmd1)

	// start graphql-engine with database point to REPLICA, if the env is set
	if hasuraGqlReadReplicaUrls != "" {
		// if HASURA_GRAPHQL_METADATA_DATABASE_URL is not specified use HASURA_GRAPHQL_DATABASE_URL
		if hasuraGqlMetadataDatabaseUrl == "" {
			hasuraGqlMetadataDatabaseUrl = hasuraGqlDatabaseUrl
		}
		log.Info("Starting graphql-engine read replica at port 8882")
		cmd2 := exec.CommandContext(ctx, "graphql-engine", "--metadata-database-url", hasuraGqlMetadataDatabaseUrl, "serve", "--server-port", "8882")
		replicaUrlList := strings.Split(hasuraGqlReadReplicaUrls, ",")
		cmd2Env := os.Environ()
		if len(replicaUrlList) > 1 {
			// if there are more than 1 urls in hasuraGqlReadReplicaUrls
			// each url in replicaUrList must be in a form of ENV_KEY=url
			cmd2.Env = append(cmd2Env, replicaUrlList...)
		} else {
			replicaUrl := replicaUrlList[0]
			// check if replicaUrl is in a form of key=value
			matched, _ := regexp.MatchString(`^\w+=`, replicaUrl)
			if matched {
				cmd2.Env = append(cmd2Env, replicaUrl)
			} else {
				// if not, assume that the replicaUrl is the database url with default
				// ENV_KEY is HASURA_GRAPHQL_DATABASE_URL
				cmd2.Env = append(cmd2Env, "HASURA_GRAPHQL_DATABASE_URL="+replicaUrl)
			}
		}
		// route the output to stdout
		stdoutReader, stdoutWriter := io.Pipe()
		cmd2.Stderr = os.Stderr
		cmd2.Stdout = stdoutWriter
		// setup buf scanner and log parser
		cmd2StdoutLines := StartScannerForBuffer(ctx, stdoutReader)
		go ParseLog(ctx, cmd2StdoutLines)
		// its ok to kill this process, as it only serve readonly queries
		// cmd2.Cancel = func() error {
		// 	return cmd2.Process.Signal(syscall.SIGTERM)
		// }
		if err := cmd2.Start(); err != nil {
			log.Error("Failed to start replica-engine:", err)
			os.Exit(1)
		}
		// call Wait() in a goroutine to avoid blocking the main thread
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd2.Wait()
		}()
		cmds = append(cmds, cmd2)
		// loop for the readonly replica-engine to start, we dont need to wait for it
		// so can run in a goroutine
		go func() {
			for {
				// check if the startupReadonlyCtx is done
				if startupReadonlyCtx.Err() != nil {
					log.Error("replica-engine is not yet ready after 60s")
					break
				}
				//sleep for 0.5s
				time.Sleep(500 * time.Millisecond)
				// fire GET request to app runner health url using fiber.GET
				agent := fiber.Get("http://localhost:8882/healthz")
				if err := agent.Parse(); err != nil {
					log.Warn(fmt.Sprintf("Startup wait readonly replica-engine server: %v", err))
				}
				code, _, _ := agent.Bytes()
				if code == 200 {
					log.Info("GraphQL Engine Plus readonly replica-engine is ready")
					startupReadonlyDoneFn()
					break
				}
			}
		}()
	}
	// wait loop for the scripting-server + primary-engine to start
	for {
		// check if the startupCtx is done
		if startupCtx.Err() != nil {
			log.Error("scripting-server or primary-engine is not yet ready after 60s")
			break
		}
		//sleep for 0.5s
		time.Sleep(500 * time.Millisecond)
		// fire GET request to app runner health url using fiber.GET
		agent := fiber.Get("http://localhost:8880/health/engine?quite=true&not=replica")
		if err := agent.Parse(); err != nil {
			log.Warn(fmt.Sprintf("Startup wait engine servers: %v", err))
		}
		code, _, errs := agent.Bytes()
		if len(errs) > 0 {
			log.Warn(fmt.Sprintf("Startup wait engine servers: %v", errs))
		}
		if code == 200 {
			startupDoneFn()
			log.Info("GraphQL Engine Plus is ready")
			break
		}
	}
	// Wait for any process to exit, does not matter if it is hasura-graphql-engine or python
	check := true
	for loop := true; loop; {
		// check if ctx is canceled while waiting
		select {
		case <-ctx.Done():
			log.Info("context is canceled: Wait for all processes to exit")
			wg.Wait()
			// check error from all cmds
			errString := ""
			for idx, cmd := range cmds {
				if cmd.ProcessState != nil {
					// use idx to set the process name
					processName := "-"
					switch idx {
					case 0:
						processName = "scripting-server"
					case 1:
						processName = "graphql-engine:primary"
					case 2:
						processName = "graphql-engine:replica"
					}
					if cmd.ProcessState.Exited() {
						log.Info("Process exited successfully [" + processName + "]")
					} else {
						if idx < 2 {
							// if the process is scripting-server or graphql-engine:primary
							// then we need to capture the error
							log.Error("Process exited with error " + cmd.ProcessState.String() + " [" + processName + "]")
							errString += cmd.ProcessState.String() + " [" + processName + "]\n"
						} else {
							// for the replica, we dont care if it is exited with error
							log.Warn("Process exited with warning " + cmd.ProcessState.String() + " [" + processName + "]")
						}
					}
				}
			}
			// send the error to the channel
			if errString != "" {
				shutdownErrorChanel <- errors.New(errString)
			} else {
				shutdownErrorChanel <- nil
			}
			close(shutdownErrorChanel)
			return
		default:
			// loop through all cmds of processes to check if any of them is exited
			if check {
				for idx, cmd := range cmds {
					if cmd.ProcessState != nil {
						// use idx to set the process name
						processName := "-"
						switch idx {
						case 0:
							processName = "scripting-server"
						case 1:
							processName = "graphql-engine:primary"
						case 2:
							processName = "graphql-engine:replica"
						}
						if cmd.ProcessState.Success() {
							log.Error("Process unexpectedly exited with code 0 [" + processName + "]")
						} else {
							log.Error("Process unexpectedly exited with error " + cmd.ProcessState.String() + " [" + processName + "]")
						}
						loop = true
						check = false
						// this will trigger graceful GraphQL Engine Plus and its engine servers
						// to be gracefully shutdown by the GracefulShutdown function
						mainCtxCancelFn()
					}
				}
			}
			time.Sleep(1 * time.Second)
		}
	}
}
