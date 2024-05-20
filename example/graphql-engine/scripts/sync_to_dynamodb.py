from aiohttp import web
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from mypy_boto3_dynamodb.service_resource import DynamoDBServiceResource

# do not import here unless for type hinting, must import in main() function


async def main(request: web.Request, body):
    import logging, time
    from datetime import datetime
    from opentelemetry import trace

    tracer = trace.get_tracer("sync_to_dynamodb.py")
    logger = logging.getLogger("sync_to_dynamodb.py")
    with tracer.start_as_current_span("sync_to_dynamodb.py") as span:
        payload = body
        BATCH_SIZE = request.query.get("BATCH_SIZE", 10)

        # require redis client to be initialized, HASURA_GRAPHQL_REDIS_URL
        # or HASURA_GRAPHQL_REDIS_CLUSTER_URL must be set in environment
        try:
            r = request.app["redis_cluster"]
        except KeyError:
            r = request.app["redis_client"]

        # require boto3 session to be initialized, by set environment
        # ENGINE_PLUS_ENABLE_BOTO3 to include 'dynamodb'
        boto3_dynamodb: DynamoDBServiceResource = request.app["boto3_dynamodb"]
        json_encoder = request.app["json_encoder"]
        json_decoder = request.app["json_decoder"]

        def convert_timestamp_fields_of_object_to_epoch(object_data):
            # scan the object_data for timestamp string fields and convert them to epoch
            for key, value in object_data.items():
                if (
                    isinstance(value, str)
                    and 19 <= len(value) <= 32
                    and value[10] == "T"
                ):
                    # TODO: handle timestamp without timezone, miliseconds
                    try:
                        object_data[key] = int(
                            datetime.strptime(
                                value, "%Y-%m-%dT%H:%M:%S.%f%z"
                            ).timestamp()
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
            request.query.get("ONLY_SYNC_CHANGES_FROM_GRAPHQL_MUTATION")
            and session_variables == trace_context == None
        ):
            # skip the JSON sync as this changes occur in the database but not from the GraphQL Engine
            return {
                "status": "skipped",
                "reason": f"changes is not caused by graphql mutation",
            }
        # get dynamodb table resources
        table_name = f"{payload['table']['schema']}.{payload['table']['name']}"
        sync_queue_name = "sync-queue:" + table_name
        delete_queue_name = "sync-queue-delete:" + table_name
        # check if the dynamodb table exist
        provisioning_waiter = None
        response = await boto3_dynamodb.meta.client.list_tables()
        if table_name in response["TableNames"]:
            logger.info(f"Table `{table_name}` already exists")
        else:
            # create a new dynamodb table
            logger.info(f"Creating table `{table_name}`")
            await boto3_dynamodb.create_table(
                TableName=table_name,
                KeySchema=[
                    {"AttributeName": "id", "KeyType": "HASH"},
                    {"AttributeName": "created_at", "KeyType": "RANGE"},
                ],
                AttributeDefinitions=[
                    {"AttributeName": "id", "AttributeType": "S"},
                    {"AttributeName": "created_at", "AttributeType": "N"},
                ],
                ProvisionedThroughput={"ReadCapacityUnits": 5, "WriteCapacityUnits": 5},
            )
            # create a waiter to wait for the table to be provisioned if required to do so.
            provisioning_waiter = boto3_dynamodb.meta.client.get_waiter("table_exists")

        if action == "INSERT" or action == "UPDATE" or action == "MANUAL":
            object_data = payload["event"]["data"]["new"]
            convert_timestamp_fields_of_object_to_epoch(object_data)
            # push it to start of the queue
            queue_len = await r.lpush(sync_queue_name, json_encoder.encode(object_data))
            # check if the queue is full and process it
            if queue_len >= BATCH_SIZE:
                # pop a batch from the queue
                # and batch write it to dynamodb
                objects_batch = await r.rpop(sync_queue_name, BATCH_SIZE)
                if objects_batch:
                    with tracer.start_as_current_span("dynamodb.batch_write_item"):
                        if provisioning_waiter:
                            await provisioning_waiter.wait(TableName=table_name)
                        # TODO: handle failure of batch write, send to dead letter queue
                        table = await boto3_dynamodb.Table(table_name)
                        async with table.batch_writer() as dynamo_writer:
                            for object_data_json_str in list(set(objects_batch)):
                                object_data = json_decoder.decode(object_data_json_str)
                                logger.info(
                                    f"Batch write object.id={object_data['id']} into table `{table_name}`"
                                )
                                # set TTL to 7 days (dynamodb uses epoch in seconds)
                                object_data["ttl"] = int(time.time()) + 7 * 24 * 60 * 60
                                await dynamo_writer.put_item(Item=object_data)
            return object_data
        elif action == "DELETE":
            object_data = payload["event"]["data"]["old"]
            convert_timestamp_fields_of_object_to_epoch(object_data)
            # delete the object from the table
            object_key = {
                "id": object_data["id"],
                "created_at": object_data["created_at"],
            }
            # push it to start of the queue
            queue_len = await r.lpush(
                delete_queue_name, json_encoder.encode(object_key)
            )
            # check if the queue is full and process it
            if queue_len >= BATCH_SIZE:
                count = 0
                # pop a batch from the queue
                # and batch write it to dynamodb
                objects_batch = await r.rpop(delete_queue_name, BATCH_SIZE)
                if objects_batch:
                    with tracer.start_as_current_span("dynamodb.batch_delete_item"):
                        if provisioning_waiter:
                            await provisioning_waiter.wait(TableName=table_name)
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
                return {"status": "deleted", "count": count}
            else:
                return {"status": "pending"}
    return {"status": "skipped", "reason": f"Unknow action={action}"}
