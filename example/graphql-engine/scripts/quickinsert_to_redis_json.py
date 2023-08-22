from aiohttp import web

# do not import here, must import in main() function


async def main(request: web.Request, body, transport):
    import copy
    from datetime import datetime
    import uuid
    import logging

    logger = logging.getLogger("quickinsert_to_redis_json.py")
    # required params from body
    payload = body["payload"]
    if "table" not in payload or "object" not in payload["input"]:
        # ignore this request
        return

    def convert_timestamp_fields_of_object_to_epoch(json_dict):
        # scan the json_dict for timestamp string fields and convert them to epoch
        for key, value in json_dict.items():
            if isinstance(value, str) and 20 <= len(value) <= 26 and value[10] == "T":
                json_dict[key] = int(
                    datetime.strptime(value, "%Y-%m-%dT%H:%M:%S.%f").timestamp() * 1000
                )

    # require redis client to be initialized in the server.py
    r = request.app["redis_client"]
    # clone the object json data in payload
    payload_input_object = payload["input"]["object"]
    payload_input_object["id"] = str(uuid.uuid4())
    object_data = payload_input_object
    # set auto timestamp key to current epoch
    now = datetime.now()
    object_data["created_at"] = int(now.timestamp() * 1000)
    # store the new record in redis JSON key=`$schema:$table:$id` using JSON.SET command
    key = f"data:{payload['table']['schema']}.{payload['table']['name']}:{object_data['id']}"
    # temp fix for redis cluster
    # await r.json().set(key, "$", object_data)

    # async redis cluster does not support json yet
    object_data["external_ref_list"] = ",".join(object_data["external_ref_list"])
    # await r.hset(key, mapping=object_data)
    # send the payload to redis stream `audit:$schema.$table:insert` using XADD command
    stream_key = f"audit:{payload['table']['schema']}.{payload['table']['name']}:insert"
    await r.xadd(stream_key, object_data, maxlen=100)

    payload_input_object["created_at"] = now.strftime("%Y-%m-%dT%H:%M:%S.%f")
    payload_input_object["updated_at"] = None
    body["payload"] = payload_input_object
