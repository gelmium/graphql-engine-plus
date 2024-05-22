from aiohttp import web
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from mypy_boto3_dynamodb.service_resource import DynamoDBServiceResource, Table
    from boto3.dynamodb.table import BatchWriter
    from redis import Redis
    from msgspec.json import Encoder, Decoder

# do not import here unless for type hinting, must import in main() function


async def main(request: web.Request, body):
    import logging, time
    from datetime import datetime
    from opentelemetry import trace

    payload = body
    SCRIPT_NAME = "sync_to_dynamodb.py"
    tracer = trace.get_tracer(SCRIPT_NAME)
    logger = logging.getLogger(SCRIPT_NAME)
    with tracer.start_as_current_span(
        SCRIPT_NAME,
        attributes={
            "trigger_id": payload.get("id"),
            "trigger_name": payload.get("trigger", {}).get("name"),
        },
    ) as span:
        BATCH_SIZE = int(request.query.get("BATCH_SIZE", 10))
        IS_CRON_JOB = request.query.get("IS_CRON_JOB")
        # require redis client to be initialized, HASURA_GRAPHQL_REDIS_URL
        # or HASURA_GRAPHQL_REDIS_CLUSTER_URL must be set in environment
        try:
            r: Redis = request.app["redis_cluster"]
        except KeyError:
            r: Redis = request.app["redis_client"]

        # require boto3 session to be initialized, by set environment
        # ENGINE_PLUS_ENABLE_BOTO3 to include 'dynamodb'
        boto3_dynamodb: DynamoDBServiceResource = request.app["boto3_dynamodb"]
        json_encoder: Encoder = request.app["json_encoder"]
        json_decoder: Decoder = request.app["json_decoder"]
        action = payload["event"]["op"]
        session_variables = payload["event"].get("session_variables")
        trace_context = payload["event"].get("trace_context")
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
            logger.debug(f"Table `{table_name}` already exists")
        else:
            # create a new dynamodb table
            logger.info(f"Creating table `{table_name}`")
            try:
                await boto3_dynamodb.create_table(
                    TableName=table_name,
                    KeySchema=[
                        {"AttributeName": "id", "KeyType": "HASH"},
                    ],
                    AttributeDefinitions=[
                        {"AttributeName": "id", "AttributeType": "S"},
                    ],
                    # on-demand capacity mode
                    BillingMode="PAY_PER_REQUEST",
                )
            except boto3_dynamodb.meta.client.exceptions.ResourceInUseException:
                logger.info(
                    f"Table `{table_name}` already exists while attempting to create"
                )
            except Exception as e:
                logger.error(f"Failed to create table `{table_name}`: {e}")
                return {
                    "status": "failed",
                    "reason": f"Failed to create table `{table_name}`",
                }
            # create a waiter to wait for the table to be provisioned if required to do so.
            provisioning_waiter = boto3_dynamodb.meta.client.get_waiter("table_exists")

        if action == "INSERT" or action == "UPDATE" or action == "MANUAL":
            if IS_CRON_JOB:
                queue_len = await r.llen(sync_queue_name)
            else:
                object_data = payload["event"]["data"]["new"]
                # push it to start of the queue
                queue_len = await r.lpush(
                    sync_queue_name, json_encoder.encode(object_data)
                )
            # check if the queue is full and process it
            if queue_len >= BATCH_SIZE:
                if provisioning_waiter:
                    await provisioning_waiter.wait(TableName=table_name)
                # pop a batch from the queue
                # and batch write it to dynamodb
                if IS_CRON_JOB:
                    pop_count = queue_len
                else:
                    pop_count = BATCH_SIZE
                objects_batch = await r.rpop(sync_queue_name, pop_count)
                if objects_batch:
                    try:
                        with tracer.start_as_current_span(
                            "dynamodb.batch_write_item",
                            attributes={
                                "table_name": table_name,
                                "batch_size": str(len(objects_batch)),
                                "ops": "put_item",
                            },
                        ):
                            count = 0
                            # TODO: handle failure of batch write, send to dead letter queue
                            table: Table = await boto3_dynamodb.Table(table_name)
                            async with table.batch_writer() as batch_writer:
                                batch_writer: BatchWriter
                                for object_data_json_str in list(set(objects_batch)):
                                    object_data = json_decoder.decode(
                                        object_data_json_str
                                    )
                                    logger.info(
                                        f"Batch write object.id={object_data['id']} into table `{table_name}`"
                                    )
                                    # set TTL to 7 days (dynamodb uses epoch in seconds for ttl field)
                                    object_data["ttl"] = (
                                        int(time.time()) + 7 * 24 * 60 * 60
                                    )
                                    await batch_writer.put_item(Item=object_data)
                                    count += 1
                            return {"status": "updated", "count": count}
                    except Exception as e:
                        # requeue the failed batch
                        await r.lpush(sync_queue_name, objects_batch)
                        logger.error(
                            f"error while batch write into table `{table_name}`: {e}"
                        )
                        return {"status": "error", "message": str(e)}
                # else (no objects in the queue)
                return {"status": "skipped", "message": "No objects in the queue"}
            # else:
            return {"status": "pending", "in_queue": queue_len}
        elif action == "DELETE":
            if IS_CRON_JOB:
                queue_len = await r.llen(delete_queue_name)
            else:
                object_data = payload["event"]["data"]["old"]
                # delete the object from the table
                object_key = {
                    "id": object_data["id"]
                }
                # push it to start of the queue
                queue_len = await r.lpush(
                    delete_queue_name, json_encoder.encode(object_key)
                )
            # check if the queue is full and process it
            if queue_len >= BATCH_SIZE:
                if provisioning_waiter:
                    await provisioning_waiter.wait(TableName=table_name)
                count = 0
                # pop a batch from the queue
                # and batch write it to dynamodb
                if IS_CRON_JOB:
                    pop_count = queue_len
                else:
                    pop_count = BATCH_SIZE
                objects_batch = await r.rpop(delete_queue_name, pop_count)
                if objects_batch:
                    try:
                        with tracer.start_as_current_span(
                            "dynamodb.batch_delete_item",
                            attributes={
                                "table_name": table_name,
                                "batch_size": str(len(objects_batch)),
                                "ops": "delete_item",
                            },
                        ):
                            table: Table = await boto3_dynamodb.Table(table_name)
                            async with table.batch_writer() as batch_writer:
                                # deduplicate, dynamodb doesnt allow batch delete_item with duplicate keys
                                for object_key_json_sr in list(set(objects_batch)):
                                    object_key = json_decoder.decode(object_key_json_sr)
                                    logger.info(
                                        f"Batch delete object.id={object_key['id']} from table `{table_name}`"
                                    )
                                    await batch_writer.delete_item(Key=object_key)
                                    count += 1
                    except Exception as e:
                        # requeue the failed batch
                        await r.lpush(delete_queue_name, objects_batch)
                        logger.error(
                            f"error while batch delete from table `{table_name}`: {e}"
                        )
                        return {"status": "error", "message": str(e)}
                return {"status": "deleted", "count": count}
            # else:
            return {"status": "pending", "in_queue": queue_len}
    return {"status": "skipped", "reason": f"Unknow action={action}"}
