import logging
import os
import random
import traceback
import aiohttp
from aiohttp import web
from aiohttp.abc import AbstractAccessLogger
import asyncio
import sys
import redis.asyncio as redis
import time
from gql_client import GqlAsyncClient
from datetime import datetime
import uvloop
import aioboto3
import contextlib
import msgspec

# global variable to keep track of async tasks
# this also prevent the tasks from being garbage collected
async_tasks = []
# global variable to cache the loaded execfile/execurl functions
exec_cache = {}
ENGINE_PLUS_ENABLE_BOTO3 = os.environ.get("ENGINE_PLUS_ENABLE_BOTO3")
# global variable to cache the boto3 session
if ENGINE_PLUS_ENABLE_BOTO3:
    boto3_session = aioboto3.Session()

# environment variables
ENGINE_PLUS_ALLOW_EXECURL = os.environ.get("ENGINE_PLUS_ALLOW_EXECURL")
ENGINE_PLUS_EXECUTE_SECRET = os.environ.get(
    "ENGINE_PLUS_EXECUTE_SECRET", os.environ["HASURA_GRAPHQL_ADMIN_SECRET"]
)

logger = logging.getLogger("scripting-server")
# Pre create json encoder/decoder for server to reuse for every request
json_encoder = msgspec.json.Encoder()
json_decoder = msgspec.json.Decoder()


async def exec_script(request: web.Request, body):
    # remote execution via proxy
    execproxy = body.get("execproxy")
    if execproxy:
        # remove the execproxy from body, to prevent infinite loop
        del body["execproxy"]
        # forward the request to execproxy
        req_body = json_encoder.encode(body)
        req_headers = {
            "Content-Type": "application/json",
            "Accept": "application/json",
            "User-Agent": "graphql-engine-plus/v1.0.0",
        }
        # loop through the request headers and copy to req_headers
        disallowed_headers = set(
            list(req_headers.keys()) + ["Host", "Content-Length", "Accept-Encoding"]
        )
        for key, value in request.headers.items():
            if key not in disallowed_headers:
                req_headers[key] = value
        async with aiohttp.ClientSession() as http_session:
            max_retries = int(body.get("execproxy_max_retries", 15))
            for i in range(1, max_retries + 1):
                async with http_session.post(
                    execproxy, data=req_body, headers=req_headers
                ) as resp:
                    text_body = await resp.text()
                    # check if the response is 200
                    if resp.status == 200:
                        try:
                            body["payload"] = json_decoder.decode(text_body)
                        except msgspec.DecodeError:
                            body["payload"] = text_body
                        break
                    elif resp.status == 429:
                        # App Runner 429 headers doesnt contain Retry-After but only x-envoy-upstream-service-time
                        # extract the "Retry-After" header
                        retry_after = resp.headers.get("Retry-After")
                        if retry_after:
                            retry_after = min(float(retry_after), 29.0)
                        else:
                            retry_after = 0.1 * i + random.random() * (3.0 + 0.3 * i)
                        logger.info(
                            f"Rate limited by App Runner, retrying after {retry_after} seconds"
                        )
                        body["status_code"] = 429
                        # retry after advised/random seconds
                        await asyncio.sleep(retry_after)
                        continue
                    else:
                        logger.error(
                            f"Error status={resp.status} in forwarding the request to {resp.url}, body={text_body}"
                        )
                        body["status_code"] = resp.status
                        raise Exception(
                            f"Error status={resp.status} in forwarding the request to {resp.url}"
                        )
            else:
                raise Exception(
                    f"Max retries exceeded ({max_retries}) when forwarding the request to {execproxy}"
                )
        # the execution is forwarded to execproxy
        # so we can return here, body["payload"] is set
        # with the result of execution from execproxy
        return
    # local script execution
    exec_main_func = None
    execfile = body.get("execfile")
    if execfile:
        exec_main_func = exec_cache.get(execfile)
        if not exec_main_func or body.get("execfresh"):
            with open(os.path.join("/graphql-engine/scripts", execfile), "r") as f:
                exec(f.read())
            try:
                exec_main_func = exec_cache[execfile] = locals()["main"]
            except KeyError:
                raise Exception(
                    "The script must define this function: `async def main(request, body):`"
                )

    # The `execurl` feature is required only if want to
    # add new script/modify existing script on the fly without deployment
    execurl = body.get("execurl")
    if ENGINE_PLUS_ALLOW_EXECURL:
        if execurl:
            # this is to increase security to prevent unauthorized script execution from URL
            req_execute_secret = request.headers.get("X-Engine-Plus-Execute-Secret")
            if not req_execute_secret:
                raise ValueError(
                    "The header X-Engine-Plus-Execute-Secret is required in request to execute script from URL (execurl)"
                )
            if req_execute_secret != ENGINE_PLUS_EXECUTE_SECRET:
                raise ValueError(
                    "The value of header X-Engine-Plus-Execute-Secret is not matched the value of ENGINE_PLUS_EXECUTE_SECRET"
                )
            exec_main_func = exec_cache.get(execurl)
            if not exec_main_func or body.get("execfresh"):
                async with aiohttp.ClientSession() as http_session:
                    async with http_session.get(
                        execurl, headers={"Cache-Control": "no-cache"}
                    ) as resp:
                        exec(await resp.text())
                exec_main_func = exec_cache[execurl] = locals()["main"]
    else:
        if execurl:
            raise Exception(
                "To execute script from URL (execurl), you must set value for these environment: ENGINE_PLUS_ALLOW_EXECURL, ENGINE_PLUS_EXECUTE_SECRET"
            )
    # the script must define this function: `async def main(request, body, transport):`
    # so it can be executed here in curent context
    if not exec_main_func:
        raise Exception(
            "At least one of these parameter must be specified: execfile, execurl"
        )
    try:
        task = asyncio.get_running_loop().create_task(exec_main_func(request, body))
    except TypeError:
        raise Exception(
            "The script must define this function: `async def main(request, body):`"
        )
    if getattr(body, "execasync", False):
        async_tasks.append(task)
        task.add_done_callback(lambda x: async_tasks.remove(task))
    else:
        await task


async def execute_code_handler(request: web.Request):
    # And execute the Python code.
    # mark starting time
    request.start_time = time.time()
    try:
        # Get the Python code from the JSON request body
        body = {}
        body = json_decoder.decode(await request.text())
        ### execute the python script in the body
        await exec_script(request, body)
        ### the script can modify the body['payload'] to transform the return data
        status_code = 200
        result = body["payload"]
    except Exception as e:
        if isinstance(e, web.HTTPException):
            raise e
        elif isinstance(e, (SyntaxError, ValueError)):
            status_code = body.get("status_code", 400)
        else:
            status_code = body.get("status_code", 500)
        result = {
            "error": str(e.__class__.__name__),
            "message": str(getattr(e, "msg", e.args[0] if len(e.args) else e)),
            "traceback": str(traceback.format_exc()),
        }
        logger.error(result)
    # Return the result of the Python code execution.
    return web.Response(
        status=status_code,
        headers={
            "Content-type": "application/json",
            "X-Execution-Time": f"{time.time() - request.start_time}",
        },
        body=json_encoder.encode(result),
    )


from typing import Any, Dict, List


class ValidationData(msgspec.Struct):
    input: List[Dict[str, Any]]


class ValidatePayload(msgspec.Struct):
    data: ValidationData
    role: str
    session_variables: Dict[str, str]
    version: int


validate_json_decoder = msgspec.json.Decoder(ValidatePayload)


async def validate_json_code_handler(request: web.Request):
    # mark starting time
    start_time = time.time()
    try:
        # get the request GET params
        params = dict(request.query)
        # Get the Python code from the params and assign to body
        payload = validate_json_decoder.decode(await request.text())
        body = {
            "payload": payload.data.input,
            "session": {
                "role": payload.role,
                "session_variables": payload.session_variables,
                "version": payload.version,
            },
        }
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
        status_code = 400
        if isinstance(e, web.HTTPException):
            raise e
        elif isinstance(e, (SyntaxError, ValueError, msgspec.ValidationError)):
            pass
        else:
            logger.error(e)
        result = {
            "message": str(getattr(e, "msg", e.args[0] if len(e.args) else e)),
        }
    # Return the result of the Python code execution.
    return web.Response(
        status=status_code,
        headers={
            "Content-type": "application/json",
            "X-Execution-Time": f"{time.time() - start_time}",
        },
        body=json_encoder.encode(result),
    )


async def healthcheck_graphql_engine(request: web.Request):
    result = {"primary": {}, "replica": {}}
    # check for GET params
    not_include = request.query.get("not", "")
    if bool(request.query.get("quite")):
        request.silent_access_log = True
    async with aiohttp.ClientSession() as http_session:
        try:
            async with http_session.get(
                "http://localhost:8881/healthz?strict=true", timeout=5
            ) as resp:
                # extract the response status code and body
                result["primary"] = {"status": resp.status, "body": await resp.text()}
            if (
                os.environ.get("HASURA_GRAPHQL_READ_REPLICA_URLS")
                and "replica" not in not_include
            ):
                async with http_session.get(
                    "http://localhost:8880/healthz", timeout=5
                ) as resp:
                    # extract the response status code and body
                    result["replica"] = {
                        "status": resp.status,
                        "body": await resp.text(),
                    }
            health_status = 200
            if result["replica"].get("status", 200) != 200:
                health_status = result["replica"]["status"]
            if result["primary"]["status"] != 200:
                health_status = result["primary"]["status"]
            return web.Response(
                status=health_status,
                headers={
                    "Content-type": "application/json",
                },
                body=json_encoder.encode(result),
            )
        except Exception as e:
            return web.Response(
                status=500,
                headers={
                    "Content-type": "application/json",
                },
                body=json_encoder.encode({"status": f"Error: {e}"}),
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
    app["json_encoder"] = json_encoder
    app["json_decoder"] = json_decoder

    # init boto3 session if enabled, this allow faster boto3 connection in scripts
    if ENGINE_PLUS_ENABLE_BOTO3:
        init_resources = ENGINE_PLUS_ENABLE_BOTO3.split(",")
        # TODO: add support for different region
        context_stack = contextlib.AsyncExitStack()
        app["boto3_context_stack"] = context_stack
        app["boto3_session"] = boto3_session
        if "dynamodb" in init_resources:
            app["boto3_dynamodb"] = await context_stack.enter_async_context(
                boto3_session.resource("dynamodb")
            )
        if "s3" in init_resources:
            app["boto3_s3"] = await context_stack.enter_async_context(
                boto3_session.resource("s3")
            )
        if "sqs" in init_resources:
            app["boto3_sqs"] = await context_stack.enter_async_context(
                boto3_session.resource("sqs")
            )

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

    # register cleanup on shutdown
    app.on_shutdown.append(cleanup_server)

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
    for name in [
        "boto",
        "urllib3",
        "s3transfer",
        "boto3",
        "botocore",
        "aioboto3",
        "aiobotocore",
    ]:
        logging.getLogger(name).setLevel(logging.ERROR)
    return app


async def cleanup_server(app):
    futures = []
    # close the redis connections
    redis_cluster = app.get("redis_cluster")
    if redis_cluster:
        print("Scripting server shutdown: Closing redis-cluster connection")
        futures.append(redis_cluster.close())
    redis_client = app.get("redis_client")
    if redis_client:
        print("Scripting server shutdown: Closing redis connection")
        futures.append(redis_client.close())
    # close boto3 session
    boto3_context_stack = app.get("boto3_context_stack")
    if boto3_context_stack:
        print("Scripting server shutdown: Closing boto3 session")
        futures.append(boto3_context_stack.aclose())
    # wait all futures
    await asyncio.gather(*futures)
    print("Scripting server shutdown: Finished")


class AccessLogger(AbstractAccessLogger):
    def log(self, request, response, response_time):
        if getattr(request, "silent_access_log", False):
            return
        latency = float(response.headers.get("X-Execution-Time", response_time))
        # start time of request in UNIX time %t
        start_time = getattr(request, "start_time", time.time() - latency)
        self.logger.info(
            f'{datetime.fromtimestamp(start_time).strftime("%Y-%m-%dT%H:%M:%S.%f")}'
            f' " {request.method.ljust(8)} {request.path}" {response.status}  {latency*1000:6f}ms'
            f' ({response.body_length}) [{request.headers.get("X-Request-ID", "")}] "{request.headers.get("Referer", "")}" "{request.headers.get("User-Agent", "")}"'
        )


if __name__ == "__main__":
    asyncio.set_event_loop_policy(uvloop.EventLoopPolicy())
    web.run_app(
        get_app(),
        host="127.0.0.1",
        port=int(sys.argv[1]) if len(sys.argv) > 1 else 8888,
        access_log_class=AccessLogger,
    )
    # try to cancel any running async tasks
    for task in async_tasks:
        if not task.done():
            task.cancel()
    print("Scripting server is gracefully shutdown")
    sys.exit(0)
