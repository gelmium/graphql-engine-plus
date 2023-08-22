import logging
import json
from types import SimpleNamespace
from gql.transport.aiohttp import AIOHTTPTransport
import os
import traceback
import aiohttp
from aiohttp import web
import json
import asyncio
import sys
import redis.asyncio as redis
import asyncpg
import time

# global variable to keep track of async tasks
# this also prevent the tasks from being garbage collected
async_tasks = []
exec_cache = {}
aio_http_transport = AIOHTTPTransport(
    url="http://localhost:8881/v1/graphql",
    headers={"x-hasura-admin-secret": os.environ["HASURA_GRAPHQL_ADMIN_SECRET"]},
)
allow_unsafe_script_execution = bool(os.environ.get("ALLOW_UNSAFE_SCRIPT_EXECUTION"))


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
        task = asyncio.get_running_loop().create_task(
            locals()["main"](request, body, aio_http_transport)
        )
    except KeyError:
        if body.get("execurl") and not allow_unsafe_script_execution:
            raise Exception(
                "To execute script from URL (execurl), must set ALLOW_UNSAFE_SCRIPT_EXECUTION=true"
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
        async with session.get('http://localhost:8881/healthz?strict=true') as resp:
            # extract the response status code and body
            return web.Response(
                status=resp.status,
                headers={
                    "Content-type": "application/json",
                },
                text=json.dumps({"status": await resp.text()}),
            )

async def get_app():
    # Create the HTTP server.
    app = web.Application()
    # init dependencies
    redis_url = os.environ.get("HASURA_GRAPHQL_REDIS_URL")
    if redis_url:
        app["redis_client"] = await redis.from_url(redis_url)
    redis_cluster_url = os.environ.get("HASURA_GRAPHQL_REDIS_CLUSTER_URL")
    if redis_cluster_url:
        app["redis_cluster"] = await redis.RedisCluster.from_url(redis_cluster_url)
        if not redis_url or redis_url == redis_cluster_url:
            app["redis_client"] = app["redis_cluster"]

    app["psql_client"] = await asyncpg.connect(
        dsn=os.environ["HASURA_GRAPHQL_DATABASE_URL"]
    )
    # add health check endpoint
    app.router.add_get("", healthcheck_graphql_engine)
    app.router.add_get("/health/engine", healthcheck_graphql_engine)
    app.router.add_get("/health", lambda x: web.Response(status=200, text="OK"))
    # add main scripting endpoint
    app.router.add_post("/", execute_code_handler)
    app.router.add_post("", execute_code_handler)
    # add validate scripting endpoint
    app.router.add_post("/validate", validate_json_code_handler)

    # Create the access logger.
    logger = logging.getLogger("aiohttp.access")
    logger.setLevel(logging.INFO)
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(message)s"))
    logging.basicConfig(
        handlers=[handler],
        level=logging.DEBUG if os.getenv("DEBUG") else logging.INFO,
    )
    return app


if __name__ == "__main__":
    web.run_app(get_app(), port=8888)
    # try to cancel any running async tasks
    for task in async_tasks:
        if not task.done():
            task.cancel()
