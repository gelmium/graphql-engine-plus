from aiohttp import web

# do not import here, must import in main() function

async def main(request: web.Request, body):
    import re
    import uuid
    from datetime import datetime
    import logging
    import asyncio

    params = body["params"]
    payload = body["payload"]
    logger = logging.getLogger("cron_sync_to_psql.py")
    INTERVAL_MS = int(params["INTERVAL_MS"])
    BATCH_SIZE = int(params["BATCH_SIZE"])
    MAX_ROUND = int(params.get("MAX_ROUND", 1))

    async def create_group(r, skey, gname):
        try:
            await r.xgroup_create(name=skey, groupname=gname, id=0)
        except Exception as e:
            logger.debug(f"Ignore error: {e}")

    def convert_field(field, field_name):
        # check if field is a epoch timestamp integer in milliseconds
        if isinstance(field, int) and (
            field_name in {"created_at", "updated_at"}
            or 10**12 <= field < 10**13
        ):
            # convert miliseconds epoch to timestamp datetime object
            return datetime.fromtimestamp(field / 1000.0)
        # other while keep as is
        return field

    async def upsert_to_psql(r, c, redis_key, redis_json_object=None):
        # get redis json from redis_key 'data:public.pba_customer:2d3169ab-3225-4df5-aa20-4b3dd6bdaf6c'
        if redis_json_object is None:
            redis_json_object = await r.json().get(redis_key)
        if not redis_json_object:
            logger.info(
                f"redis_json_object for redis_key {redis_key} is not exist, skip"
            )
            return
        # upsert to psql using asyncpg
        db_table = redis_key.split(":")[1]
        fields = list(redis_json_object.keys())
        index_of_id = fields.index("id")
        raw_sql = f"""
            INSERT INTO {db_table} as EXISTING ({','.join(fields)})
            VALUES ({','.join(['$'+str(i+1) for i in range(len(fields))])})
            ON CONFLICT (id) DO 
              UPDATE SET ({','.join(fields)}) = ({','.join(['$'+str(i+1) for i in range(len(fields))])})
              WHERE 
                EXISTING.id = {'$'+str(index_of_id+1)} AND (
                  (EXCLUDED.created_at IS NOT NULL AND EXISTING.created_at IS NULL) OR 
                  (EXCLUDED.updated_at IS NOT NULL AND (EXISTING.updated_at IS NULL OR EXISTING.updated_at < EXCLUDED.updated_at))
                )
            ;"""
        logger.debug(f"execute raw_sql: {raw_sql}")
        await c.execute(
            raw_sql,
            *[convert_field(redis_json_object[f], f) for f in fields],
        )

    async def delete_if_not_exist_psql(r, c, redis_key):
        # check if redis_key exists in redis
        # if not exists, delete from psql
        result = await r.json().get(redis_key)
        if result is None:
            # delete from psql using asyncpg
            db_table = redis_key.split(":")[1]
            pk = redis_key.split(":")[-1]
            raw_sql = f"""
                DELETE FROM {db_table}
                WHERE id = '{pk}'
                ;"""
            logger.debug(f"execute raw_sql: {raw_sql}")
            await c.execute(raw_sql)
        else:
            # probaly this is JSON.DELETE single property with path
            # in this case, we can update the psql row
            # TODO: may need to add the field=None redis_json_object
            await upsert_to_psql(r, c, redis_key, redis_json_object=result)

    async def replies_handler(r, c, replies, group_name):
        for d_stream in replies:
            for element in d_stream[1]:
                stream_id = d_stream[0]
                msg_id = element[0]
                logger.info(f"got msg_id: {msg_id} from stream {stream_id}")
                stream_msg = element[1]
                logger.debug(f"stream_msg: {stream_msg}")
                if msg_id is None or stream_msg is None:
                    continue
                redis_key = stream_msg[b"redis_key"].decode()
                if not redis_key.startswith("data:"):
                    logger.debug(f"ignore redis_key: {redis_key}")
                    continue
                try:
                    redis_command = stream_msg[b"redis_command"].decode()
                    if redis_command in {"del", "json.del"}:
                        await delete_if_not_exist_psql(r, c, redis_key)
                    else:
                        await upsert_to_psql(r, c, redis_key)
                    logger.debug(f"xack: {stream_id}, {group_name}, {msg_id}")
                    await r.xack(stream_id, group_name, msg_id)
                except Exception as e:
                    logger.error(f"Error: {e}")
                    continue

    group_name = "sync-to-psql-consumer"
    stream_key = "keyspace:json.set"
    stream_key2 = "keyspace:del"
    stream_key3 = "keyspace:json.del"
    r = request.app["redis_client"]
    c = request.app["psql_client"]
    await create_group(r, stream_key, group_name)
    await create_group(r, stream_key2, group_name)
    await create_group(r, stream_key3, group_name)
    # random uuid for consumer name
    consumer_name = f"sync-to-psql-consumer-{payload['id']}"
    logger.info(f"Starting consumer {consumer_name} ...")
    # check for pending replies
    pending_info = await r.xpending(stream_key, group_name)
    # {'pending': 1, 'min': b'1690699314142-0', 'max': b'1690699314142-0', 'consumers': [{'name': b'sync-to-psql-consumer-26b824a7-8dcf-48be-a7e9-2f818b01b66d', 'pending': 1}]}
    logger.info(f"pending_info: {pending_info}")
    if pending_info["pending"] > 0:
        # autoclaim pending replies what was idle for more than INTERVAL_MS
        single_stream_replies = await r.xautoclaim(
            stream_key,
            group_name,
            consumer_name,
            INTERVAL_MS,
            start_id=pending_info["min"].decode(),
            count=BATCH_SIZE,
        )
        logger.debug(f"autoclaim replies: {single_stream_replies}")
        # override stream_id so we can use the same handler
        single_stream_replies[0] = stream_key
        try:
            await replies_handler(r, c, [single_stream_replies], group_name)
        except Exception as e:
            logger.error(f"Error: {e}")
    else:
        pending_info["min"] = int(datetime.now().timestamp() * 1000)
    try:
        for i in range(0, MAX_ROUND):
            replies = await r.xreadgroup(
                groupname=group_name,
                consumername=consumer_name,
                block=INTERVAL_MS,
                count=BATCH_SIZE,
                streams={
                    stream_key: ">",
                    stream_key2: ">",
                    stream_key3: ">",
                },
            )
            await replies_handler(r, c, replies, group_name)
            # logger.info("Looping ...")
    except asyncio.CancelledError:
        logger.info("Graceful exiting ...")
    finally:  # alway try to delete the consumer
        delete_list = []
        stream_key_list = [stream_key, stream_key2, stream_key3]
        async with r.pipeline(transaction=False) as pipe:
            for stream_key in stream_key_list:
                # check if this consumer contain pending replies
                await pipe.xpending_range(
                    stream_key,
                    group_name,
                    pending_info["min"],
                    "+",
                    1,
                    consumername=consumer_name,
                )
            pipe_replies = await pipe.execute()
            for i in range(len(pipe_replies)):
                consumer_pending_info = pipe_replies[i]
                stream_key = stream_key_list[i]
                if len(consumer_pending_info) > 0:
                    logger.info(
                        f"Consumer {consumer_name} has pending replies in stream: {stream_key}, cannot delete"
                    )
                else:
                    delete_list.append((stream_key, group_name, consumer_name))
            if len(delete_list):
                for stream_key, group_name, consumer_name in delete_list:
                    await pipe.xgroup_delconsumer(stream_key, group_name, consumer_name)
                    logger.info(
                        f"Deleted consumer {consumer_name} in stream: {stream_key}"
                    )
                await pipe.execute()
