from aiohttp import web

# do not import here, must import in main() function


async def main(request: web.Request, body):
    from datetime import datetime, timezone
    import uuid, logging, msgspec

    logger = logging.getLogger("quickinsert_to_redis_json.py")
    # required params from body
    params = body["params"]
    payload = body["payload"]
    STREAM_MAX_LEN = params.get("STREAM_MAX_LEN", 100000)
    if "table" not in payload or "object" not in payload["input"]:
        # ignore this request
        return

    # require redis client to be initialized in the server.py
    try:
        r = request.app["redis_cluster"]
    except KeyError:
        r = request.app["redis_client"]
    # clone the object json data in payload
    payload_input_object = payload["input"]["object"]
    payload_input_object["id"] = str(uuid.uuid4())
    # set auto timestamp key to current epoch
    now = datetime.now(timezone.utc).astimezone()
    payload_input_object["created_at"] = now.isoformat('T')
    payload_input_object["updated_at"] = None
    # send the payload to redis stream `audit:$schema.$table:insert` using XADD command
    stream_key = (
        f"worker:{payload['table']['schema']}.{payload['table']['name']}:insert"
    )
    logger.info(f"Push object.id={payload_input_object['id']} to stream: {stream_key}")
    await r.xadd(
        stream_key,
        {
            "payload": msgspec.json.encode(payload_input_object),
        },
        maxlen=STREAM_MAX_LEN,
    )

    body["payload"] = payload_input_object
    return
