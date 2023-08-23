// FILEPATH: /workspaces/graphql-engine-plus/src/proxy/container_start.go
package main

import (
	"os"
	"os/exec"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

// setup a fiber app which contain a simple /health endpoint which return a 200 status code
func Setup() *fiber.App {
	app := fiber.New(
		fiber.Config{
			ReadTimeout:  60 * time.Second,
			WriteTimeout: 60 * time.Second,
			ServerHeader: "App Runner Demo",
		},
	)
	app.Use(logger.New(logger.Config{
		Format:     "${time} \"${method} ${path}\" ${status} ${latency} ${ip}\n",
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
		// fire a POST request to the upstream url using the same header and body from the original request
		agent := fiber.Post("http://localhost:8881/v1/graphql")

		// loop through the header and set the header from the original request
		c.Request().Header.VisitAll(func(key, value []byte) {
			agent.Request().Header.SetBytesKV(key, value)
		})
		// set the body from the original request
		agent.Request().SetBody(c.Request().Body())
		// send the request to the upstream url using Fiber Go
		if err := agent.Parse(); err != nil {
			panic(err)
		}
		code, body, errs := agent.Bytes()
		if len(errs) > 0 {
			panic(errs)
		}
		// return the response from the upstream url
		return c.Status(code).Send(body)
	})

	return app
}

func StartServers() {
	// create an empty list of cmds
	var cmds []*exec.Cmd
	// start scripting server at port 8888
	cmd := exec.Command("bash", "-c", "cd /graphql-engine/scripting/ && python3 server.py &")
	if err := cmd.Start(); err != nil {
		log.Error("Error starting scripting server:", err)
		os.Exit(1)
	}
	cmds = append(cmds, cmd)

	// start graphql-engine schema v1
	cmd = exec.Command("bash", "-c", "graphql-engine serve --enable-console false --server-port 8881")
	if err := cmd.Start(); err != nil {
		log.Error("Error starting graphql-engine schema v1:", err)
		os.Exit(1)
	}
	cmds = append(cmds, cmd)

	// start graphql-engine with database point to REPLICA, if the env is set
	if os.Getenv("HASURA_GRAPHQL_READ_REPLICA_URLS") != "" {
		cmd = exec.Command("bash", "-c", "graphql-engine serve --enable-console false --server-port 8880 --database-url \"$HASURA_GRAPHQL_READ_REPLICA_URLS\"")
		if err := cmd.Start(); err != nil {
			log.Error("Error starting graphql-engine schema v2:", err)
			os.Exit(1)
		}
		cmds = append(cmds, cmd)
	}

	// start graphql-engine schema v2, if the env is set
	if os.Getenv("HASURA_GRAPHQL_METADATA_DATABASE_URL_V2") != "" {
		cmd = exec.Command("bash", "-c", "graphql-engine serve --enable-console false --server-port 8882 --metadata-database-url \"$HASURA_GRAPHQL_METADATA_DATABASE_URL_V2\"")
		if err := cmd.Start(); err != nil {
			log.Error("Error starting graphql-engine schema v2:", err)
			os.Exit(1)
		}
		cmds = append(cmds, cmd)
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

	app := Setup()
	go app.Listen(":8000")
	StartServers()
}
