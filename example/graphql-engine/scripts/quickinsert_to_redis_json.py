from aiohttp import web
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from redis import Redis
    from msgspec.json import Encoder, Decoder
# do not import here unless for type hinting, must import in main() function


async def main(request: web.Request, env):
    from datetime import datetime, timezone
    import uuid, logging
    from aiohttp.web import Response

    logger = logging.getLogger("quickinsert_to_redis_json.py")
    # required params from query string
    table_schema = "public"
    table_name = "customer"
    STREAM_MAX_LEN = request.query.get("stream_max_len", 100000)
    # read body
    payload = await request.json()
    if "object" not in payload["input"]:
        # ignore this request
        return

    # require redis client to be initialized in the server.py
    r: Redis = env["redis_cluster"]
    if r is None:
        r: Redis = env["redis_client"]

    json_encoder: Encoder = env["json_encoder"]
    # clone the object json data in payload
    payload_input_object = payload["input"]["object"]
    payload_input_object["id"] = str(uuid.uuid4())
    # set auto timestamp key to current epoch
    now = datetime.now(timezone.utc).astimezone()
    payload_input_object["created_at"] = now.isoformat("T")
    payload_input_object["updated_at"] = None
    # send the payload to redis stream `audit:$schema.$table:insert` using XADD command
    stream_key = f"worker:{table_schema}.{table_name}:insert"
    logger.info(f"Push object.id={payload_input_object['id']} to stream: {stream_key}")
    input_object_json = json_encoder.encode(payload_input_object)
    await r.xadd(
        stream_key,
        {
            "input_object": input_object_json,
        },
        maxlen=STREAM_MAX_LEN,
    )
    response = Response(
        status=201,
        content_type="application/json",
        body=input_object_json
    )
    return response
