from aiohttp import web

# do not import here, must import in main() function

async def main(request: web.Request, body, transport):
    import json
    from datetime import datetime
    # required params from body
    # 10000 entries size is approx 10MB
    params = body['params']
    payload = body['payload']
    STREAM_MAX_LEN = params['STREAM_MAX_LEN']

    def convert_timestamp_fields_of_object_to_epoch(object_data):
        # scan the object_data for timestamp string fields and convert them to epoch
        for key, value in object_data.items():
            if isinstance(value, str) and 20 <= len(value) <= 26 and value[10] == "T":
                object_data[key] = int(
                    datetime.strptime(value, "%Y-%m-%dT%H:%M:%S.%f").timestamp() * 1000
                )
    # require redis client to be initialized in the server.py
    r = request.app["redis_client"]
    async with r.pipeline(transaction=True) as pipe:
        # send the payload to redis stream `audit:$schema:$table` using XADD command
        stream_id = (
            f"audit:{payload['table']['schema']}.{payload['table']['name']}"
        )
        await pipe.xadd(
            stream_id,
            {
                "created_at": payload["created_at"],
                "current_retry": payload["delivery_info"]["current_retry"],
                "max_retries": payload["delivery_info"]["max_retries"],
                "event": json.dumps(payload["event"]),
                "id": payload["id"],
                "table_name": payload["table"]["name"],
                "db_schema": payload["table"]["schema"],
                "trigger_name": payload["trigger"]["name"],
            },
            maxlen=STREAM_MAX_LEN,
        )
        action = payload["event"]["op"]
        session_variables = payload["event"]["session_variables"]
        trace_context = payload["event"]["trace_context"]
        if session_variables == trace_context == None:
            # skip the JSON sync as this changes occur in the database but not from the GraphQL Engine
            return
        if action == "INSERT" or action == "UPDATE" or action == "MANUAL":
            object_data = payload["event"]["data"]["new"]
            auto_timestamp_key = "created_at" if action == "INSERT" else "updated_at"
            if action == "MANUAL" and payload["event"]["data"]["old"] == None:
                # this is a manual insert, so we need to set the `created_at` timestamp
                auto_timestamp_key = "created_at"
            convert_timestamp_fields_of_object_to_epoch(object_data)
            # set auto timestamp key to current epoch
            object_data[auto_timestamp_key] = int(datetime.now().timestamp() * 1000)
            # store the new record in redis JSON key=`$schema:$table:$id` using JSON.SET command
            key = f"data:{payload['table']['schema']}.{payload['table']['name']}:{object_data['id']}"
            await pipe.json().set(key, "$", object_data)
        elif action == "DELETE":
            object_data = payload["event"]["data"]["old"]
            # delete the object_data from redis JSON key=`$schema:$table:$id` using JSON.DEL command
            key = f"data:{payload['table']['schema']}.{payload['table']['name']}:{object_data['id']}"
            await pipe.json().delete(key, "$")

        await pipe.execute()
