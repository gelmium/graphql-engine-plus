from aiohttp import web

# do not import here, must import in main() function


async def main(request: web.Request, body):
    import os, logging
    import aioboto3
    from datetime import datetime

    logger = logging.getLogger("sync_to_dynamodb.py")
    # required params from body
    # 10000 entries size is approx 10MB
    params = body["params"]
    payload = body["payload"]

    def convert_timestamp_fields_of_object_to_epoch(object_data):
        # scan the object_data for timestamp string fields and convert them to epoch
        for key, value in object_data.items():
            if isinstance(value, str) and 19 <= len(value) <= 32 and value[10] == "T":
                # TODO: handle timestamp without timezone, miliseconds
                object_data[key] = int(
                    datetime.strptime(value, "%Y-%m-%dT%H:%M:%S.%f%z").timestamp()
                    * 1000
                )

    # setup async boto3 connection session
    session = aioboto3.Session()
    async with session.resource(
        "dynamodb", region_name=os.environ["AWS_DEFAULT_REGION"]
    ) as dynamo_resource:
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
        table = await dynamo_resource.Table(table_name)
        if action == "INSERT" or action == "UPDATE" or action == "MANUAL":
            object_data = payload["event"]["data"]["new"]
            convert_timestamp_fields_of_object_to_epoch(object_data)
            # insert the object into the table
            # if the object already exist, it will be overwritten
            logger.info(f"Inserting object into table `{table_name}`: {object_data}")
            await table.put_item(Item=object_data)
        elif action == "DELETE":
            object_data = payload["event"]["data"]["old"]
            convert_timestamp_fields_of_object_to_epoch(object_data)
            # delete the object from the table
            object_key = {
                "id": object_data["id"],
                "created_at": object_data["created_at"],
            }
            logger.info(
                f"Delete object.id={object_data['id']} from table `{table_name}`"
            )
            await table.delete_item(Key=object_key)
        body["payload"] = object_data
        return
