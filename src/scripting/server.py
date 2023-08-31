import logging
import json
import os
import traceback
import aiohttp
from aiohttp import web
from aiohttp.abc import AbstractAccessLogger
import json
import asyncio
import sys
import redis.asyncio as redis
import asyncpg
import time
from gql_client import GqlAsyncClient
import re
from datetime import datetime

# global variable to keep track of async tasks
# this also prevent the tasks from being garbage collected
async_tasks = []
exec_cache = {}

allow_unsafe_script_execution = bool(os.environ.get("ENGINE_PLUS_ALLOW_UNSAFE_SCRIPT_EXECUTION"))


async def exec_script(request, body):
    execfile = body.get("execfile")
    if execfile:
        execfile_content = exec_cache.get(execfile)
        if not execfile_content or body.get("execfresh"):
            with open(os.path.join("/graphql-engine/scripts", execfile), "r") as f:
                execfile_content = f.read()
            exec_cache[execfile] = execfile_content
        exec(execfile_content)

    # The below functionality is required only if want to
    # add/modify script on the fly without deployment
    if allow_unsafe_script_execution:
        execurl = body.get("execurl")
        if execurl:
            execurl_content = exec_cache.get(execurl)
            if not execurl_content or body.get("execfresh"):
                async with aiohttp.ClientSession() as session:
                    async with session.get(execurl) as resp:
                        execurl_content = await resp.text()
                exec_cache[execurl] = execurl_content
            exec(execurl_content)
    # the script must define this function: `async def main(request, body, transport):`
    # so it can be executed here in curent context
    try:
        task = asyncio.get_running_loop().create_task(locals()["main"](request, body))
    except KeyError:
        if body.get("execurl") and not allow_unsafe_script_execution:
            raise Exception(
                "To execute script from URL (execurl), must set ENGINE_PLUS_ALLOW_UNSAFE_SCRIPT_EXECUTION=true"
            )
        else:
            raise Exception(
                "The script must define this function: `async def main(request, body, transport):`"
            )
    except TypeError:
        raise Exception(
            "The script must define this function: `async def main(request, body, transport):`"
        )
    if getattr(body, "execasync", False):
        async_tasks.append(task)
        task.add_done_callback(lambda x: async_tasks.remove(task))
    else:
        await task


async def execute_code_handler(request: web.Request):
    # And execute the Python code.
    # mark starting time
    start_time = time.time()
    try:
        # Get the Python code from the JSON request body
        body = json.loads(await request.text())
        ### execute the python script in the body
        await exec_script(request, body)
        ### the script can modify the body['payload'] to transform the return data
        status_code = 200
        result = body["payload"]
    except Exception as e:
        if isinstance(e, (SyntaxError, ValueError)):
            status_code = 400
        else:
            status_code = 500
        result = {
            "error": str(e.__class__.__name__),
            "message": str(
                getattr(e, "msg", e.args[0] if len(e.args) else "Unknown error")
            ),
            "traceback": str(traceback.format_exc()),
        }
        logger = logging.getLogger("aiohttp.error")
        logger.error(result)
    # Return the result of the Python code execution.
    return web.Response(
        status=status_code,
        headers={
            "Content-type": "application/json",
            "X-Execution-Time": f"{time.time() - start_time}",
        },
        text=json.dumps(result),
    )


async def validate_json_code_handler(request: web.Request):
    # mark starting time
    start_time = time.time()
    try:
        # get the request GET params
        params = dict(request.query)
        # Get the Python code from the params and assign to body
        payload = json.loads(await request.text())
        body = {"payload": payload}
        body["execfile"] = params.get("execfile")
        body["execurl"] = params.get("execurl")
        body["execasync"] = params.get("execasync")
        body["execfresh"] = params.get("execfresh")
        ### execute the python script in the body
        await exec_script(request, body)
        ### the script can modify the body['payload'] to transform the return data
        status_code = 200
        result = body["payload"]
    except Exception as e:
        if isinstance(e, (SyntaxError, ValueError)):
            status_code = 400
        else:
            status_code = 500
        result = {
            "error": str(e.__class__.__name__),
            "message": str(
                getattr(e, "msg", e.args[0] if len(e.args) else "Unknown error")
            ),
            "traceback": str(traceback.format_exc()),
        }
        logger = logging.getLogger("aiohttp.error")
        logger.error(result)
        if isinstance(e, ValueError):
            # for Validation Error, remove the error, traceback
            del result["error"]
            del result["traceback"]
    # Return the result of the Python code execution.
    return web.Response(
        status=status_code,
        headers={
            "Content-type": "application/json",
            "X-Execution-Time": f"{time.time() - start_time}",
        },
        text=json.dumps(result),
    )


async def healthcheck_graphql_engine(request: web.Request):
    async with aiohttp.ClientSession() as session:
        try:
            async with session.get("http://localhost:8881/healthz?strict=true") as resp:
                # extract the response status code and body
                return web.Response(
                    status=resp.status,
                    headers={
                        "Content-type": "application/json",
                    },
                    text=json.dumps({"status": await resp.text()}),
                )
        except Exception as e:
            return web.Response(
                status=500,
                headers={
                    "Content-type": "application/json",
                },
                text=json.dumps({"status": f"Error: {e}"}),
            )


async def get_app():
    # Create the HTTP server.
    app = web.Application()
    # init dependencies
    redis_cluster_url = os.environ.get("HASURA_GRAPHQL_REDIS_CLUSTER_URL")
    redis_url = os.environ.get("HASURA_GRAPHQL_REDIS_URL")
    if redis_cluster_url:
        app["redis_cluster"] = await redis.RedisCluster.from_url(redis_cluster_url)
    if redis_url:
        app["redis_client"] = await redis.from_url(redis_url)

    app["graphql_client"] = GqlAsyncClient()
    app["psql_client"] = await asyncpg.connect(
        dsn=os.environ["HASURA_GRAPHQL_DATABASE_URL"]
    )
    replica_db_url = os.environ.get("HASURA_GRAPHQL_READ_REPLICA_URLS")
    if replica_db_url:
        # only use the first replica db url even if there are multiple
        app["psql_readonly"] = await asyncpg.connect(dsn=replica_db_url.split(",")[0])

    # add health check endpoint
    app.router.add_get("", healthcheck_graphql_engine)
    app.router.add_get("/health/engine", healthcheck_graphql_engine)
    app.router.add_get("/health", lambda x: web.Response(status=200, text="OK"))
    # add main scripting endpoint
    app.router.add_post("/execute", execute_code_handler)
    app.router.add_post("/", execute_code_handler)
    app.router.add_post("", execute_code_handler)
    # add validate scripting endpoint
    app.router.add_post("/validate", validate_json_code_handler)

    # Create the access log handler for aiohttp
    nullify_handler = logging.NullHandler()
    if not os.getenv("DEBUG"):
        # disable the gql.transport.aiohttp logger
        lg = logging.getLogger("gql.transport.aiohttp")
        lg.addHandler(nullify_handler)
        lg.propagate = False

    accesslog_handler = logging.StreamHandler(sys.stdout)
    accesslog_handler.setFormatter(logging.Formatter("%(message)s"))
    access_lg = logging.getLogger("aiohttp.access")
    access_lg.addHandler(accesslog_handler)
    access_lg.propagate = False

    default_handler = logging.StreamHandler(sys.stdout)
    default_handler.setFormatter(
        logging.Formatter(
            "%(asctime)s %(levelname)s %(name)s %(message)s",
            # datefmt="%Y-%m-%dT%H:%M:%S.uuuuuu",
        )
    )
    app_log_level = logging.DEBUG if bool(os.getenv("DEBUG")) else logging.INFO
    default_handler.setLevel(app_log_level)
    logging.basicConfig(
        handlers=[default_handler],
        level=app_log_level,
    )
    # set log level for boto logger to disable boto logs
    for name in ["boto", "urllib3", "s3transfer", "boto3", "botocore", "aioboto3", "aiobotocore"]:
        logging.getLogger(name).setLevel(logging.ERROR)
    return app


async def cleanup_server(app):
    # close the redis connections
    if app["redis_cluster"]:
        print("Scripting server shutdown: Closing redis-cluster connection")
        await app["redis_cluster"].close()
    if app["redis_client"]:
        print("Scripting server shutdown: Closing redis connection")
        await app["redis_client"].close()
    # close the psql connections
    print("Scripting server shutdown: Closing psql connection")
    await app["psql_client"].close()


class AccessLogger(AbstractAccessLogger):
    def log(self, request, response, response_time):
        # start time of request in UNIX time %t
        start_time = time.time() - response_time
        self.logger.info(
            f'{datetime.fromtimestamp(start_time).strftime("%Y-%m-%dT%H:%M:%S.%f")}'
            f' " {request.method.ljust(8)} {request.path}" {response.status}  {response_time*1000:6f}ms'
            f' ({response.body_length}) "{request.headers.get("Referer", "")}" "{request.headers.get("User-Agent", "")}"'
        )


if __name__ == "__main__":
    # access_log='"%r" %s %Tf (%b) "%{Referer}i" "%{User-Agent}i"'
    web.run_app(get_app(), host="127.0.0.1", port=8888, access_log_class=AccessLogger)
    # try to cancel any running async tasks
    for task in async_tasks:
        if not task.done():
            task.cancel()
    print("Scripting server is gracefully shutdown")
    os.exit(0)
