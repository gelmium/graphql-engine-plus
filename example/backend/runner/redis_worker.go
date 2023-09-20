package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/xid"
)

type RedisStreamMessage struct {
	MessageId          string
	MessageData        map[string]interface{}
	StreamName         string
	ConsumersGroupName string
}

func SetupRedisWorker(ctx context.Context, gshutdownChanel chan error) {
	var avgTime int64 = 10_000_000 // 10ms in nanoseconds unit
	var totalCount int64 = 1
	// set this flag to True will tell the redis worker to
	// fire health request before finish processing the message
	preFireHealthReq, err := strconv.ParseBool(os.Getenv("PREFIRE_HEALTH_REQUEST"))
	if err != nil {
		preFireHealthReq = false
	}
	// create a channel of int64
	responseTimeChan := make(chan int64)
	// worker configuration
	concurencyLevel := 10
	_concurencyLevel, err := strconv.ParseInt(os.Getenv("CONCURRENCY_LEVEL"), 10, 0)
	if err == nil {
		concurencyLevel = int(_concurencyLevel)
	}
	// reading appRunnerHealthUrl from environment variable
	// this endpoint act as a mean to trigger scaling up of App Runner service
	appRunnerHealthUrl := os.Getenv("AWS_APP_RUNNER_HEALTH_ENDPOINT_URL")
	var redisClient redis.UniversalClient
	// reading clusterUrl from environment variable
	clusterUrl := os.Getenv("REDIS_CLUSTER_URL")
	if clusterUrl != "" {
		clusterOpt, err := redis.ParseClusterURL(clusterUrl)
		if err != nil {
			panic(err)
		}
		clusterOpt.TLSConfig.InsecureSkipVerify = true
		redisClient = redis.NewClusterClient(clusterOpt)
	} else {
		opt, err := redis.ParseURL(os.Getenv("REDIS_URL"))
		if err != nil {
			panic(err)
		}
		redisClient = redis.NewClient(opt)
	}
	// ping redis server
	err = redisClient.Ping(ctx).Err()
	if err != nil {
		panic(err)
	}
	if clusterUrl != "" {
		log.Info("Connected to Redis Cluster Server")
	} else {
		log.Info("Connected to Redis Server")
	}
	// create a new postgresql connection pool
	postgresClient, err := pgxpool.New(context.Background(), os.Getenv("POSTGRES_DATABASE_URL"))
	if err != nil {
		panic(err)
	}
	log.Info("Connected to Postgres Database")

	stream := "worker:public.customer:insert"
	consumersGroup := "test-go-consumer-group"

	// create stream and consumer group automatically if not exist
	err = redisClient.XGroupCreateMkStream(ctx, stream, consumersGroup, "0").Err()
	if err != nil {
		log.Warn(err)
	}

	uniqueID := xid.New().String()
	redisMsgChan := SetupMessageHandler(ctx, concurencyLevel, redisClient, postgresClient, responseTimeChan)
	// create a goroutine to handle the response time come from the MessageHandler
	go func() {
		for {
			select {
			case <-ctx.Done():
				// if ctx is cancelled, exit the goroutine
				return
			// wait for the response time from the channel
			case resTime := <-responseTimeChan:
				// recalculate the average response time
				avgTime = (avgTime*totalCount + resTime) / (totalCount + 1)
				totalCount++
				if !preFireHealthReq {
					// post fire health request using the exact response time
					go func() {
						// fire GET request to app runner health url using fiber.GET
						agent := fiber.Get(appRunnerHealthUrl)
						agent.QueryString("sleep=" + strconv.FormatInt(resTime/1000, 10))
						if err := agent.Parse(); err != nil {
							log.Error(err)
						}
						_, _, errs := agent.Bytes()
						if len(errs) > 0 {
							log.Error(errs)
						}
					}()
				}
				break
			}
		}
	}()
	log.Info(fmt.Sprintf("Consumer Id=%s is ready with concurencyLevel=%d", uniqueID, concurencyLevel))
	lastPendingMsgCount := 0
	// start main loop to receive message from redis
	for {
		// check context for cancellation
		select {
		case <-ctx.Done():
			log.Info("Context cancelled, delete consumer id from consumer group")
			// first check if there is any pending message still belong to this consumer
			if lastPendingMsgCount != 0 {
				log.Warn("This consumer may have pending messages, it should be check and delete manually")
			} else {
				// delete consumer id from consumer group
				err = redisClient.XGroupDelConsumer(context.Background(), stream, consumersGroup, uniqueID).Err()
				if err != nil {
					log.Error(err)
				} else {
					log.Info("Deleted from consumer group, consumer id: ", uniqueID)
				}
			}
			// gracefully shutdown postgres connection pool
			log.Info("Context cancelled, close postgres client")
			postgresClient.Close()
			// gracefully shutdown redis client
			log.Info("Context cancelled, close redis client")
			gshutdownChanel <- redisClient.Close()
			close(gshutdownChanel)
			return
		default:
		}
		var messageList []redis.XMessage
		msgCount := 0

		entries, err := redisClient.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    consumersGroup,
			Consumer: uniqueID,
			Streams:  []string{stream, ">"},
			Count:    int64(concurencyLevel),               // read up to n messages at a time
			Block:    time.Duration(10 * time.Millisecond), // wait max 10ms each iteration
			NoAck:    false,
		}).Result()
		if err != nil {
			// check if "redis: nil" in error message
			if strings.Contains(err.Error(), "redis: nil") {
				// no new message, continue to process pending message if there is
			} else {
				log.Error(err)
			}
		} else {
			// reading from entries[0] as we are only reading from one stream
			// for reading multiple streams, we need to loop through entries
			// entries[0].Stream == stream
			// log.Debug(entries)
			for i := 0; i < len(entries[0].Messages); i++ {
				msg := entries[0].Messages[i]
				if msg.ID != "" && len(msg.Values) > 0 {
					messageList = append(messageList, msg)
				}
			}
			msgCount = len(messageList)
		}
		// XAutoClaim message from the stream and process the pending message
		// this allow us to retry any msg that has not been acked (pending msg)
		// A safe MinIdle value is 60 seconds, this value must be greater than
		// the time taken to process a message, or we risk duplicate message
		xAutoClaimCmd := redisClient.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   stream,
			Group:    consumersGroup,
			Consumer: uniqueID,
			MinIdle:  60 * time.Second,
			Start:    "-",
			Count:    int64(concurencyLevel - msgCount),
		})
		if xAutoClaimCmd.Err() != nil {
			log.Error(xAutoClaimCmd.Err())
		}
		// check if there is any message to process
		pendingMsgList, _, err := xAutoClaimCmd.Result()
		if err != nil {
			log.Error(err)
		}
		lastPendingMsgCount = len(pendingMsgList)
		for i := 0; i < lastPendingMsgCount; i++ {
			pendingMsg := pendingMsgList[i]
			if pendingMsg.ID != "" && len(pendingMsg.Values) > 0 {
				messageList = append(messageList, pendingMsg)
			}
		}
		msgCount = len(messageList)
		// check if there is any message to process
		if msgCount == 0 {
			continue
		}

		log.Info("Received msg count=" + strconv.Itoa(msgCount))
		if lastPendingMsgCount > 0 {
			log.Info("     pending count=" + strconv.Itoa(lastPendingMsgCount))
		}
		for i := 0; i < msgCount; i++ {
			// send the redis message to the channel
			redisMsgChan <- RedisStreamMessage{
				MessageId:          messageList[i].ID,
				MessageData:        messageList[i].Values,
				StreamName:         stream,
				ConsumersGroupName: consumersGroup,
			}
			// the chanel is buffered, so it will not block until max concurencyLevel is reached
			// then it will block here until the message can be processed

			// load simulation to AWS App Runner service
			if preFireHealthReq {
				// use app runner health url to simulate request latency to App Runner
				// if preFire flag is set, fire the request first using avgTime as the sleep time
				// otherwise, the request will be fired after the message is processed
				go func() {
					// fire GET request to app runner health url using fiber.GET
					agent := fiber.Get(appRunnerHealthUrl)
					// set Get QueryString
					// sleep parameter is in microseconds
					// convert avgTime from nanoseconds to microseconds
					agent.QueryString("sleep=" + strconv.FormatInt(avgTime/1000, 10))
					// send the request to the upstream url using Fiber Go
					if err := agent.Parse(); err != nil {
						log.Error(err)
					}
					_, _, errs := agent.Bytes()
					if len(errs) > 0 {
						log.Error(errs)
					}
				}()
			} // end load simulation
		} // end for loop enqueue message to channel
	} // end main loop receive message from redis
}
