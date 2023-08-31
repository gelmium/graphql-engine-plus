from aiohttp import web

# do not import here, must import in main() function


async def main(request: web.Request, body):
    import os, logging, time, json
    import aioboto3
    from datetime import datetime

    # mark start time in microseconds using time.time()
    start_time = int(time.time() * 1000000)
    logger = logging.getLogger("sync_to_dynamodb.py")
    # required params from body
    # 10000 entries size is approx 10MB
    params = body["params"]
    payload = body["payload"]
    BATCH_SIZE = params.get("BATCH_SIZE", 10)
    # require redis client to be initialized in the server.py
    try:
        r = request.app["redis_cluster"]
    except KeyError:
        r = request.app["redis_client"]

    def convert_timestamp_fields_of_object_to_epoch(object_data):
        # scan the object_data for timestamp string fields and convert them to epoch
        for key, value in object_data.items():
            if isinstance(value, str) and 19 <= len(value) <= 32 and value[10] == "T":
                # TODO: handle timestamp without timezone, miliseconds
                object_data[key] = int(
                    datetime.strptime(value, "%Y-%m-%dT%H:%M:%S.%f%z").timestamp()
                    * 1000
                )

    # pre-fire request to AWS App Runner to warm up the service
    aws_app_runner_health_url = params.get("AWS_APP_RUNNER_HEALTH_ENDPOINT_URL")
    if aws_app_runner_health_url:
        import aiohttp, asyncio

        async def exec_async_func(sleep_microseconds, then_func):
            async with aiohttp.ClientSession() as http_session:
                async with http_session.get(
                    aws_app_runner_health_url + f"?sleep={sleep_microseconds}"
                ) as resp:
                    then_func(resp)
        SHARED_VARS = globals().get(
            "SYNC_TO_DYNAMODB_SHARED_VARIABLES",
            {
                "avg_duration": 1000000,  # 1s in microseconds
                "count": 1,
            },
        )
        default_sleep = SHARED_VARS["avg_duration"]
        warm_app_task = asyncio.get_running_loop().create_task(
            exec_async_func(
                default_sleep,
                lambda resp: logger.info(
                    f"Fire warm-up request {default_sleep/1000000:.3f}s = {resp.status}"
                ),
            )
        )

    action = payload["event"]["op"]
    session_variables = payload["event"]["session_variables"]
    trace_context = payload["event"]["trace_context"]
    if (
        params.get("ONLY_SYNC_CHANGES_FROM_GRAPHQL")
        and session_variables == trace_context == None
    ):
        # skip the JSON sync as this changes occur in the database but not from the GraphQL Engine
        body["payload"] = {}
        return
    # get dynamodb table resources
    table_name = f"{payload['table']['schema']}.{payload['table']['name']}"
    sync_queue_name = "sync-queue:" + table_name
    delete_queue_name = "sync-queue-delete:" + table_name
    if action == "INSERT" or action == "UPDATE" or action == "MANUAL":
        object_data = payload["event"]["data"]["new"]
        convert_timestamp_fields_of_object_to_epoch(object_data)
        # set TTL to 7 days (dynamodb uses epoch in seconds)
        object_data["ttl"] = int(time.time()) + 7 * 24 * 60 * 60
        # push it to start of the queue
        queue_len = await r.lpush(sync_queue_name, json.dumps(object_data))
        # check if the queue is full and process it
        if queue_len >= BATCH_SIZE:
            # setup async boto3 connection session
            session = aioboto3.Session()
            async with session.resource(
                "dynamodb", region_name=os.environ["AWS_DEFAULT_REGION"]
            ) as dynamo_resource:
                table = await dynamo_resource.Table(table_name)
                async with table.batch_writer() as dynamo_writer:
                    # pop a batch from the queue
                    # and batch write it to dynamodb
                    for object_data_json_str in await r.rpop(
                        sync_queue_name, BATCH_SIZE
                    ):
                        object_data = json.loads(object_data_json_str)
                        logger.info(
                            f"Batch write object.id={object_data['id']} into table `{table_name}`"
                        )
                        await dynamo_writer.put_item(Item=object_data)
    elif action == "DELETE":
        object_data = payload["event"]["data"]["old"]
        convert_timestamp_fields_of_object_to_epoch(object_data)
        # delete the object from the table
        object_key = {
            "id": object_data["id"],
            "created_at": object_data["created_at"],
        }
        # push it to start of the queue
        queue_len = await r.lpush(delete_queue_name, json.dumps(object_key))
        # check if the queue is full and process it
        if queue_len >= BATCH_SIZE:
            # setup async boto3 connection session
            async with session.resource(
                "dynamodb", region_name=os.environ["AWS_DEFAULT_REGION"]
            ) as dynamo_resource:
                table = await dynamo_resource.Table(table_name)
                async with table.batch_writer() as dynamo_writer:
                    # pop a batch from the queue
                    # and batch write it to dynamodb
                    for object_key_json_sr in await r.rpop(
                        delete_queue_name, BATCH_SIZE
                    ):
                        object_key = json.loads(object_key_json_sr)
                        logger.info(
                            f"Batch delete object.id={object_key['id']} from table `{table_name}`"
                        )
                        await dynamo_writer.delete_item(Key=object_key)
    if aws_app_runner_health_url:
        duration = int(time.time() * 1000000) - start_time
        # recalculate the average duration
        SHARED_VARS = globals().get("SYNC_TO_DYNAMODB_SHARED_VARIABLES", SHARED_VARS)
        globals()["SYNC_TO_DYNAMODB_SHARED_VARIABLES"] = {
            "avg_duration": (
                SHARED_VARS["avg_duration"] * SHARED_VARS["count"] + duration
            )
            / (SHARED_VARS["count"] + 1),
            "count": SHARED_VARS["count"] + 1,
        }
        # wait for the warm app health request to finish
        await warm_app_task
    body["payload"] = object_data
    return
