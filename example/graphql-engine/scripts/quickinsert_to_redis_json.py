from aiohttp import web

# do not import here, must import in main() function


async def main(request: web.Request, body):
    from datetime import datetime
    import uuid, logging

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
    try:
        r = request.app["redis_cluster"]
    except KeyError:
        r = request.app["redis_client"]
    # clone the object json data in payload
    payload_input_object = payload["input"]["object"]
    payload_input_object["id"] = str(uuid.uuid4())
    object_data = payload_input_object
    # set auto timestamp key to current epoch
    now = datetime.now()
    object_data["created_at"] = int(now.timestamp() * 1000)
    object_data["external_ref_list"] = ",".join(object_data["external_ref_list"])
    # send the payload to redis stream `audit:$schema.$table:insert` using XADD command
    stream_key = (
        f"worker:{payload['table']['schema']}.{payload['table']['name']}:insert"
    )
    logger.info(f"Push object.id={object_data['id']} to stream: {stream_key}")
    await r.xadd(stream_key, object_data, maxlen=100000)

    payload_input_object["created_at"] = now.strftime("%Y-%m-%dT%H:%M:%S.%f")
    payload_input_object["updated_at"] = None
    body["payload"] = payload_input_object
    return
