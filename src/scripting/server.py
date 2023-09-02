import logging
import json
import os
import random
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
from datetime import datetime

# global variable to keep track of async tasks
# this also prevent the tasks from being garbage collected
async_tasks = []
exec_cache = {}

ENGINE_PLUS_EXECUTE_SECRET = os.environ.get("ENGINE_PLUS_EXECUTE_SECRET")
logger = logging.getLogger("scripting-server")

async def exec_script(request: web.Request, body):
    # remote execution via proxy
    execproxy = body.get("execproxy")
    if execproxy:
        # remove the execproxy from body, to prevent infinite loop
        del body["execproxy"]
        # forward the request to execproxy
        req_body = json.dumps(body)
        req_headers={
            "Content-Type": "application/json",
            "Accept": "application/json",
            "User-Agent": "graphql-engine-plus/v1.0.0",
        }
        # loop through the request headers and copy to req_headers
        disallowed_headers = set(list(req_headers.keys()) + ['Host', 'Content-Length', 'Accept-Encoding'])
        for key, value in request.headers.items():
            if key not in disallowed_headers:
                req_headers[key] = value
        async with aiohttp.ClientSession() as http_session:
            max_retries = int(body.get("execproxy_429maxretry", 15))
            for i in range(max_retries):
                async with http_session.post(execproxy, data=req_body, headers=req_headers) as resp:
                    text_body = await resp.text()
                    # check if the response is 200
                    if resp.status == 200:
                        try:
                            body["payload"] = json.loads(text_body)
                        except json.decoder.JSONDecodeError:
                            body["payload"] = text_body
                        break
                    elif resp.status == 429:
                        # App Runner 429 headers doesnt contain Retry-After but only x-envoy-upstream-service-time
                        # extract the "Retry-After" header
                        retry_after = resp.headers.get('Retry-After')
                        if retry_after:
                            retry_after = min(float(retry_after), 29.0)
                        else:
                            retry_after = 0.1*i + random.random() * (3.0+0.3*i)
                        logger.info(f"Rate limited by App Runner, retrying after {retry_after} seconds")
                        # retry after advised/random seconds
                        await asyncio.sleep(retry_after)
                        continue
                    else:
                        logger.error(f"Error status={resp.status} in forwarding the request to {resp.url}, body={text_body}")
                        raise Exception(f"Error status={resp.status} in forwarding the request to {resp.url}")
            else:
                raise Exception(f"Max retries exceeded ({max_retries}) when forwarding the request to {execproxy}")
        # the execution is forwarded to execproxy
        # so we can return here, body["payload"] is set 
        # with the result of execution from execproxy 
        return
    # local script execution
    execfile = body.get("execfile")
    if execfile:
        execfile_content = exec_cache.get(execfile)
        if not execfile_content or body.get("execfresh"):
            with open(os.path.join("/graphql-engine/scripts", execfile), "r") as f:
                execfile_content = f.read()
            exec_cache[execfile] = execfile_content
        exec(execfile_content)

    # The `execurl` feature is required only if want to
    # add new script/modify existing script on the fly without deployment
    if ENGINE_PLUS_EXECUTE_SECRET:
        # this is to increase security to prevent unauthorized script execution from URL
        if request.headers.get("X-Engine-Plus-Execute-Secret") != ENGINE_PLUS_EXECUTE_SECRET:
            raise Exception(
                "The header X-Engine-Plus-Execute-Secret is required in request to execute script from URL (execurl)"
            )
        execurl = body.get("execurl")
        if execurl:
            execurl_content = exec_cache.get(execurl)
            if not execurl_content or body.get("execfresh"):
                async with aiohttp.ClientSession() as http_session:
                    async with http_session.get(execurl, headers={'Cache-Control': 'no-cache'}) as resp:
                        execurl_content = await resp.text()
                exec_cache[execurl] = execurl_content
            exec(execurl_content)
    else:
        if body.get("execurl"):
            raise Exception(
                "To execute script from URL (execurl), you must set a value for this environment: ENGINE_PLUS_EXECUTE_SECRET"
            )
    # the script must define this function: `async def main(request, body, transport):`
    # so it can be executed here in curent context
    try:
        task = asyncio.get_running_loop().create_task(locals()["main"](request, body))
    except KeyError:
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
    async with aiohttp.ClientSession() as http_session:
        try:
            async with http_session.get("http://localhost:8881/healthz?strict=true") as resp:
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
