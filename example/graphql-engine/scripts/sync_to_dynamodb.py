from aiohttp import web

# do not import here, must import in main() function


async def main(request: web.Request, body):
    import logging, time
    from datetime import datetime

    logger = logging.getLogger("sync_to_dynamodb.py")
    # required params from body
    params = body["params"]
    payload = body["payload"]
    BATCH_SIZE = params.get("BATCH_SIZE", 10)

    # require redis client to be initialized, HASURA_GRAPHQL_REDIS_URL
    # or HASURA_GRAPHQL_REDIS_CLUSTER_URL must be set in environment
    try:
        r = request.app["redis_cluster"]
    except KeyError:
        r = request.app["redis_client"]

    # require boto3 session to be initialized, by set environment
    # ENGINE_PLUS_ENABLE_BOTO3 to include 'dynamodb'
    boto3_dynamodb = request.app["boto3_dynamodb"]
    json_encoder = request.app["json_encoder"]
    json_decoder = request.app["json_decoder"]

    def convert_timestamp_fields_of_object_to_epoch(object_data):
        # scan the object_data for timestamp string fields and convert them to epoch
        for key, value in object_data.items():
            if isinstance(value, str) and 19 <= len(value) <= 32 and value[10] == "T":
                # TODO: handle timestamp without timezone, miliseconds
                try:
                    object_data[key] = int(
                        datetime.strptime(value, "%Y-%m-%dT%H:%M:%S.%f%z").timestamp()
                        * 1000
                    )
                except ValueError:
                    # try without miliseconds
                    object_data[key] = int(
                        datetime.strptime(value, "%Y-%m-%dT%H:%M:%S%z").timestamp()
                        * 1000
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
        queue_len = await r.lpush(sync_queue_name, json_encoder.encode(object_data))
        # check if the queue is full and process it
        if queue_len >= BATCH_SIZE:
            # pop a batch from the queue
            # and batch write it to dynamodb
            objects_batch = await r.rpop(sync_queue_name, BATCH_SIZE)
            if objects_batch:
                table = await boto3_dynamodb.Table(table_name)
                async with table.batch_writer() as dynamo_writer:
                    for object_data_json_str in objects_batch:
                        object_data = json_decoder.decode(object_data_json_str)
                        logger.info(
                            f"Batch write object.id={object_data['id']} into table `{table_name}`"
                        )
                        await dynamo_writer.put_item(Item=object_data)
        body["payload"] = object_data
    elif action == "DELETE":
        object_data = payload["event"]["data"]["old"]
        convert_timestamp_fields_of_object_to_epoch(object_data)
        # delete the object from the table
        object_key = {
            "id": object_data["id"],
            "created_at": object_data["created_at"],
        }
        # push it to start of the queue
        queue_len = await r.lpush(delete_queue_name, json_encoder.encode(object_key))
        # check if the queue is full and process it
        if queue_len >= BATCH_SIZE:
            count = 0
            # pop a batch from the queue
            # and batch write it to dynamodb
            objects_batch = await r.rpop(delete_queue_name, BATCH_SIZE)
            if objects_batch:
                table = await boto3_dynamodb.Table(table_name)
                async with table.batch_writer() as dynamo_writer:
                    # deduplicate, dynamodb doesnt allow batch delete_item with duplicate keys
                    for object_key_json_sr in list(set(objects_batch)):
                        object_key = json_decoder.decode(object_key_json_sr)
                        logger.info(
                            f"Batch delete object.id={object_key['id']} from table `{table_name}`"
                        )
                        await dynamo_writer.delete_item(Key=object_key)
                        count += 1
            body["payload"] = {"status": "deleted", "count": count}
        else:
            body["payload"] = {"status": "pending"}
    return
