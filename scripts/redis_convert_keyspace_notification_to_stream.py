#!/bin/python3
import sys
import asyncio
import uvloop
import re
import redis.asyncio as redis

STREAM_MAX_LEN = 1000000  # 1 million entries approx 100MB per stream


async def pipe_reader(r, pubsub: redis.client.PubSub, batch_size):
    # using redis.pipe to batch xadd commands together
    xadd_count = 0
    count = 0
    async with r.pipeline(transaction=False) as pipe:
        while True:
            message = await pubsub.get_message(ignore_subscribe_messages=True)
            count += 1
            if message is None:
                # be nice to the CPU
                await asyncio.sleep(0.001)
            else:
                # {'type': 'pmessage', 'pattern': b'__key*__:json.*', 'channel': b'__keyevent@0__:json.del', 'data': b'data:public.pba_customer:d89430d9-0468-4c54-b16e-18d86114e1b0'}

                # only process pmessage
                if message["type"] != "pmessage":
                    continue
                msg_channel = message["channel"].decode()
                print(f"(Pubsub Reader) Message received in channel: `{msg_channel}`")
                # only convert to Stream msg if msg_channel is keyspace notification
                if re.match(r"__keyevent@\d+__:\w+\.?\w*", msg_channel):
                    redis_key = message["data"].decode()
                    redis_command = msg_channel.split(":")[1]
                    await pipe.xadd(
                        f"keyspace:{redis_command}",
                        {
                            "redis_key": redis_key,
                            "redis_command": redis_command,
                            "msg_channel": msg_channel,
                        },
                        maxlen=STREAM_MAX_LEN,
                    )
                    xadd_count += 1
            # execute the pipe
            if xadd_count >= batch_size:
                print(f"count: {count}, execute pending xadd_count: {xadd_count}")
                await pipe.execute()
                xadd_count = 0
            elif count % 3000 == 0 and xadd_count > 0:  #  every 3 seconds, execute the pipe
                print(f"count: {count}, execute pending xadd_count: {xadd_count}")
                await pipe.execute()
                xadd_count = 0


async def reader(r, pubsub: redis.client.PubSub):
    while True:
        message = await pubsub.get_message(ignore_subscribe_messages=True)
        if message is None:
            # be nice to the CPU
            await asyncio.sleep(0.001)
        else:
            # {'type': 'pmessage', 'pattern': b'__key*__:json.*', 'channel': b'__keyevent@0__:json.del', 'data': b'data:public.pba_customer:d89430d9-0468-4c54-b16e-18d86114e1b0'}

            # only process pmessage
            if message["type"] != "pmessage":
                continue
            msg_channel = message["channel"].decode()
            print(f"(Pubsub Reader) Message received in channel: `{msg_channel}`")
            # only convert to Stream msg if msg_channel is keyspace notification
            if re.match(r"__keyevent@\d+__:\w+\.?\w*", msg_channel):
                redis_key = message["data"].decode()
                redis_command = msg_channel.split(":")[1]
                await r.xadd(
                    f"keyspace:{redis_command}",
                    {
                        "redis_key": redis_key,
                        "redis_command": redis_command,
                        "msg_channel": msg_channel,
                    },
                    maxlen=STREAM_MAX_LEN,
                )
            elif re.match(r"__keyspace@\d+__:\w+\.?\w*", msg_channel):
                redis_command = message["data"].decode()
                redis_key = msg_channel.split(":")[1]
                await r.xadd(
                    f"keyspace:{redis_command}",
                    {
                        "redis_key": redis_key,
                        "redis_command": redis_command,
                        "msg_channel": msg_channel,
                    },
                    maxlen=STREAM_MAX_LEN,
                )


async def main(args):
    try:
        redis_url = args[0]  # "redis://localhost:6379/0"
        channel_pattern = args[1]  # "__keyevent*__:json.*"
        batch_size = (
            int(args[2]) if len(args) >= 3 else 0
        )  # batch message together to increase throughput
    except:
        print(
            "Usage: python3 redis_convert_keyspace_notification_to_stream.py <redis_url> <channel_pattern>"
        )
        print(
            'ex: python3 redis_convert_keyspace_notification_to_stream.py redis://localhost:6379/0 "__keyevent*__:json.*"'
        )
        sys.exit(1)
    print(f"Connecting to {redis_url} ...")
    r = await redis.from_url(redis_url)
    async with r.pubsub() as pubsub:
        print(f"Subscribing to `{channel_pattern}`")
        await pubsub.psubscribe(channel_pattern)
        # create task for reader to run in background
        if batch_size > 0:
            future = asyncio.create_task(pipe_reader(r, pubsub, batch_size))
        else:
            future = asyncio.create_task(reader(r, pubsub))
        print("Server is started! Waiting for messages...")
        await future


if __name__ == "__main__":
    asyncio.set_event_loop_policy(uvloop.EventLoopPolicy())
    # get all command line arguments except the first one and pass to main
    asyncio.run(main(sys.argv[1:]))
