from aiohttp import web
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from redis import Redis
    from msgspec.json import Encoder, Decoder
# do not import here unless for type hinting, must import in main() function


async def main(request: web.Request, body):
    from datetime import datetime, timezone
    import uuid, logging

    logger = logging.getLogger("quickinsert_to_redis_json.py")
    # required params from query string
    table_schema = "public"
    table_name = "customer"
    STREAM_MAX_LEN = request.query.get("stream_max_len", 100000)
    if "object" not in body["input"]:
        # ignore this request
        return

    # require redis client to be initialized in the server.py
    try:
        r: Redis = request.app["redis_cluster"]
    except KeyError:
        try:
            r: Redis = request.app["redis_client"]
        except KeyError:
            # ignore this request since there is no redis client configured
            return
    json_encoder: Encoder = request.app["json_encoder"]
    # clone the object json data in payload
    payload_input_object = body["input"]["object"]
    payload_input_object["id"] = str(uuid.uuid4())
    # set auto timestamp key to current epoch
    now = datetime.now(timezone.utc).astimezone()
    payload_input_object["created_at"] = now.isoformat("T")
    payload_input_object["updated_at"] = None
    # send the payload to redis stream `audit:$schema.$table:insert` using XADD command
    stream_key = f"worker:{table_schema}.{table_name}:insert"
    logger.info(f"Push object.id={payload_input_object['id']} to stream: {stream_key}")
    await r.xadd(
        stream_key,
        {
            "payload": json_encoder.encode(payload_input_object),
        },
        maxlen=STREAM_MAX_LEN,
    )
    return payload_input_object
