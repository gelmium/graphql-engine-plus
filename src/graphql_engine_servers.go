package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
)

func StartGraphqlEngineServers(ctx context.Context, mainCtxCancelFn context.CancelFunc, startupCtx context.Context, startupDoneFn context.CancelFunc, shutdownErrorChanel chan error) {
	// create an empty list of cmds
	var cmds []*exec.Cmd
	// create a waitgroup to wait for all cmds to finish
	var wg sync.WaitGroup
	// start scripting server at port 8888
	log.Info("Starting scripting-server at port 8888")
	cmd0 := exec.CommandContext(context.WithValue(ctx, "name", "scripting-server"), "python3", "/graphql-engine/scripting/server.py")
	// route the output to stdout
	cmd0.Stdout = os.Stdout
	// override the cancel function to send sigterm instead of kill
	cmd0.Cancel = func() error {
		return cmd0.Process.Signal(syscall.SIGTERM)
	}
	if err := cmd0.Start(); err != nil {
		log.Error("Error starting scripting server:", err)
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
	cmd1.Stdout = os.Stdout
	// override the cancel function to send sigterm instead of kill
	cmd1.Cancel = func() error {
		return cmd1.Process.Signal(syscall.SIGTERM)
	}
	if err := cmd1.Start(); err != nil {
		log.Error("Error starting graphql-engine schema v1:", err)
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
	if os.Getenv("HASURA_GRAPHQL_READ_REPLICA_URLS") != "" {
		metadataDatabaseUrl := os.Getenv("HASURA_GRAPHQL_METADATA_DATABASE_URL")
		if metadataDatabaseUrl == "" {
			metadataDatabaseUrl = os.Getenv("HASURA_GRAPHQL_DATABASE_URL")
		}
		log.Info("Starting graphql-engine read replica at port 8880")
		cmd2 := exec.CommandContext(context.WithValue(ctx, "name", "graphql-ro-server"), "graphql-engine", "--metadata-database-url", metadataDatabaseUrl, "serve", "--server-port", "8880")
		cmd2.Env = append(os.Environ(), "HASURA_GRAPHQL_DATABASE_URL="+os.Getenv("HASURA_GRAPHQL_READ_REPLICA_URLS"))
		// route the output to stdout
		cmd2.Stdout = nil
		// override the cancel function to send sigterm instead of kill
		cmd2.Cancel = func() error {
			return cmd2.Process.Signal(syscall.SIGTERM)
		}
		if err := cmd2.Start(); err != nil {
			log.Error("Error starting graphql-engine read replica:", err)
			os.Exit(1)
		}
		// call Wait() in a goroutine to avoid blocking the main thread
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd2.Wait()
		}()
		cmds = append(cmds, cmd2)
	}
	// wait loop for the servers to start
	for {
		// check if the startupCtx is done
		if startupCtx.Err() != nil {
			log.Error("scripting server or graphql engines is not yet ready after 60s")
			break
		}
		// fire GET request to app runner health url using fiber.GET
		agent := fiber.Get("http://localhost:8888/health/engine")
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
		} else {
			//sleep for 1s
			time.Sleep(1 * time.Second)
		}
	}
	// Wait for any process to exit, does not matter if it is graphql-engine, python or nginx
	check := true
	for loop := true; loop; {
		// check if ctx is canceled while waiting
		select {
		case <-ctx.Done():
			log.Info("context is canceled: Wait for all processes to exit")
			wg.Wait()
			// check error from all cmds
			errString := ""
			for _, cmd := range cmds {
				if cmd.ProcessState != nil {
					// concat the args to get the process name
					processName := strings.Join(cmd.Args[:min(len(cmd.Args), 4)], " ")
					if cmd.ProcessState.Exited() {
						log.Info("Process exited successfully [" + processName + "]")
					} else {
						log.Info("Process exited with error " + cmd.ProcessState.String() + " [" + processName + "]")
						errString += cmd.ProcessState.String() + "\n"
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
			loop = false
			check = false
			return
		default:
			// loop through all cmds of processes to check if any of them is exited
			if check {
				for _, cmd := range cmds {
					if cmd.ProcessState != nil {
						// concat the args to get the process name
						processName := strings.Join(cmd.Args[:min(len(cmd.Args), 4)], " ")
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
