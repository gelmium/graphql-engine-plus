// FILEPATH: /workspaces/graphql-engine-plus/src/proxy/container_start.go
package main

import (
	"context"
	"os"
	"os/exec"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

// setup a fiber app which contain a simple /health endpoint which return a 200 status code
func Setup(ctx context.Context, startupCtx context.Context) *fiber.App {
	app := fiber.New(
		fiber.Config{
			ReadTimeout:  60 * time.Second,
			WriteTimeout: 60 * time.Second,
			ServerHeader: "Graphql Engine Plus",
		},
	)
	app.Use(logger.New(logger.Config{
		Format:     "${time} \"${method} ${path}\" ${status} ${latency} (${bytesSent}) \"${reqHeader:Referer}\" \"${reqHeader:User-Agent}\"\n",
		TimeFormat: "2006/01/02 15:04:05.000000",
	}))
	app.Get("/public/graphql/health", func(c *fiber.Ctx) error {
		// read GET parameter from the request
		all := c.Query("all")
		if all != "" {
			// fire GET request to app runner health url using fiber.GET
			agent := fiber.Get("http://localhost:8888/health/engine")
			if err := agent.Parse(); err != nil {
				log.Error(err)
			}
			code, body, errs := agent.Bytes()
			if len(errs) > 0 {
				log.Error(errs)
			}
			// return the response from the upstream url
			return c.Status(code).Send(body)
		}
		return c.SendStatus(fiber.StatusOK)
	})

	// add a POST endpoint to forward request to an upstream url
	app.Post("/public/graphql/v1", func(c *fiber.Ctx) error {
		// check and wait for startupCtx to be done
		if startupCtx.Err() == nil {
			log.Warn("Waiting for startup to be completed!")
			// this wait can last for max 60s
			<-startupCtx.Done()
		}

		// fire a POST request to the upstream url using the same header and body from the original request
		agent := fiber.Post("http://localhost:8881/v1/graphql")
		// loop through the header and set the header from the original request
		for k, v := range c.GetReqHeaders() {
			agent.Set(k, v)
		}
		// set the vscode-file://vscode-app/Applications/Visual%20Studio%20Code.app/Contents/Resources/app/out/vs/code/electron-sandbox/workbench/workbench.htmlbody from the original request
		agent.Body(c.Body())
		// set the timeout to 60s
		agent.Timeout(60 * time.Second)
		// send the request to the upstream url using Fiber Go
		if err := agent.Parse(); err != nil {
			log.Error(err)
			return c.Status(500).SendString("Internal Server Error")
		}
		code, body, errs := agent.Bytes()
		if len(errs) > 0 {
			log.Error(errs)
			return c.Status(500).SendString("Internal Server Error")
		}
		// return the response from the upstream url
		return c.Status(code).Send(body)
	})

	return app
}

func StartServers(ctx context.Context, startupCtx context.Context, startupDoneFn context.CancelFunc) {
	// create an empty list of cmds
	var cmds []*exec.Cmd
	// start scripting server at port 8888
	cmd := exec.Command("bash", "-c", "cd /graphql-engine/scripting/ && python3 server.py &")
	// route the output to stdout
	cmd.Stdout = os.Stdout
	if err := cmd.Start(); err != nil {
		log.Error("Error starting scripting server:", err)
		os.Exit(1)
	}
	cmds = append(cmds, cmd)

	// start graphql-engine schema v1
	cmd = exec.Command("bash", "-c", "graphql-engine serve --server-port 8881")
	// route the output to stdout
	cmd.Stdout = os.Stdout
	if err := cmd.Start(); err != nil {
		log.Error("Error starting graphql-engine schema v1:", err)
		os.Exit(1)
	}
	cmds = append(cmds, cmd)

	// start graphql-engine with database point to REPLICA, if the env is set
	if os.Getenv("HASURA_GRAPHQL_READ_REPLICA_URLS") != "" {
		cmd = exec.Command("bash", "-c", "graphql-engine serve --server-port 8880 --database-url \"$HASURA_GRAPHQL_READ_REPLICA_URLS\"")
		// route the output to stdout
		cmd.Stdout = os.Stdout
		if err := cmd.Start(); err != nil {
			log.Error("Error starting graphql-engine schema v2:", err)
			os.Exit(1)
		}
		cmds = append(cmds, cmd)
	}

	// start graphql-engine schema v2, if the env is set
	if os.Getenv("HASURA_GRAPHQL_METADATA_DATABASE_URL_V2") != "" {
		cmd = exec.Command("bash", "-c", "graphql-engine serve --server-port 8882 --metadata-database-url \"$HASURA_GRAPHQL_METADATA_DATABASE_URL_V2\"")
		// route the output to stdout
		cmd.Stdout = os.Stdout
		if err := cmd.Start(); err != nil {
			log.Error("Error starting graphql-engine schema v2:", err)
			os.Exit(1)
		}
		cmds = append(cmds, cmd)
	}

	// wait loop for the servers to start
	for {
		// check if the startupCtx is done
		if startupCtx.Err() != nil {
			log.Error("GraphQL Engines is not yet ready after 60s")
			break
		}
		// fire GET request to app runner health url using fiber.GET
		agent := fiber.Get("http://localhost:8888/health/engine")
		if err := agent.Parse(); err != nil {
			log.Error(err)
		}
		code, _, errs := agent.Bytes()
		if len(errs) > 0 {
			log.Error(errs)
		}
		if code == 200 {
			startupDoneFn()
			log.Info("GraphQL Engines is ready")
			break
		} else {
			//sleep for 1s
			time.Sleep(1 * time.Second)
		}
	}
	// Wait for any process to exit, does not matter if it is graphql-engine, python or nginx
	for {
		for _, cmd := range cmds {
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				log.Error("Process exited:", cmd.ProcessState)
				os.Exit(1)
			}
		}
		time.Sleep(1 * time.Second)
	}
}

func main() {
	ctx := context.Background()
	startupCtx, startupDoneFn := context.WithTimeout(ctx, 60*time.Second)
	app := Setup(ctx, startupCtx)
	go app.Listen(":8000")
	StartServers(ctx, startupCtx, startupDoneFn)
}
