import inspect
import logging
import os
import random
import traceback
import argparse
import aiohttp
from aiohttp import web
from aiohttp.abc import AbstractAccessLogger
import asyncio
import sys
import aiohttp.typedefs
import redis.asyncio as redis
import time
from gql_client import GqlAsyncClient
from datetime import datetime
import uvloop
import aioboto3
import contextlib
import msgspec
from typing import Any, Dict, List, TypeVar
from collections.abc import AsyncGenerator
from contextlib import asynccontextmanager
from opentelemetry import trace
from opentelemetry.sdk.trace import Span
from opentelemetry import propagate
from opentelemetry.propagators.textmap import Getter
from opentelemetry.instrumentation.aiohttp_client import create_trace_config
import socket
from exec_cache import InternalExecCache, BASE_SCRIPTS_PATH

# global variable to keep track of async tasks
# this also prevent the tasks from being garbage collected
async_tasks = []
# global variable to cache the loaded execfile/execurl functions
exec_cache = InternalExecCache()
# make sure the scripts path exists
os.makedirs(BASE_SCRIPTS_PATH, exist_ok=True)
# add the scripts path to sys.path
sys.path.append(BASE_SCRIPTS_PATH)
# environment variables
ENGINE_PLUS_ENABLE_BOTO3 = os.getenv("ENGINE_PLUS_ENABLE_BOTO3")
if ENGINE_PLUS_ENABLE_BOTO3:
    # this is a global variable to reuse the boto3 session
    boto3_session = aioboto3.Session(region_name=os.getenv("AWS_REGION"))

ENGINE_PLUS_ALLOW_EXECURL = os.getenv("ENGINE_PLUS_ALLOW_EXECURL")
ENGINE_PLUS_EXECUTE_SECRET = os.getenv("ENGINE_PLUS_EXECUTE_SECRET")
DEBUG_MODE = os.getenv("DEBUG_MODE", "").lower() in {"true", "t", "1"}

logger = logging.getLogger("scripting-server")
# Pre create json encoder/decoder for server to reuse for every request
json_encoder = msgspec.json.Encoder()
json_decoder = msgspec.json.Decoder()

# setup telemetry
otel_exporter_type = os.getenv("ENGINE_PLUS_ENABLE_OPEN_TELEMETRY", "")
ENABLE_OTEL = True
if otel_exporter_type == "grpc":
    from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
elif otel_exporter_type == "http":
    from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
else:
    ENABLE_OTEL = False

if ENABLE_OTEL:
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import BatchSpanProcessor

    provider = TracerProvider()
    processor = BatchSpanProcessor(OTLPSpanExporter())
    provider.add_span_processor(processor)
    # Sets the global default tracer provider
    trace.set_tracer_provider(provider)
else:
    # Sets NoOpTracerProvider as the global default tracer provider
    trace.set_tracer_provider(trace.NoOpTracerProvider())


class AiohttpRequestGetter(Getter):
    def set(self, carrier: web.Request, key: str, value: str):
        carrier.headers[key] = value

    def get(self, carrier: web.Request, key: str):
        value = carrier.headers.get(key, None)
        if value is None:
            # idkw they implement like this, but when header is not present we must return None
            # instead of empty list or [None]
            return None
        else:
            # must return a list contain the value (not the value itself)
            return [value]

    def keys(self, carrier: web.Request):
        return list(carrier.headers.keys())


# Creates a tracer from the global tracer provider
tracer = trace.get_tracer("graphql-engine-plus")
# Create propagators with default value from environment
propagators = propagate.get_global_textmap()
# Create a custom getter for aiohttp request
requestGetter = AiohttpRequestGetter()


@asynccontextmanager
async def start_as_current_span_async(
    *args: Any,
    request: web.Request = None,
    **kwargs: Any,
) -> AsyncGenerator[Span]:
    """Start a new span and set it as the current span.

    Args:
        *args: Arguments to pass to the tracer.start_as_current_span method
        request: web.Request passing this will context extraction
        **kwargs: Keyword arguments to pass to the tracer.start_as_current_span method

    Yields:
        None
    """
    attrs = kwargs.get("attributes", {})
    # do context extraction only if request is passed and ENABLE_OTEL is enabled
    if ENABLE_OTEL and request:
        if not kwargs.get("context"):
            kwargs["context"] = propagators.extract(request, getter=requestGetter)
        # set the http request attributes to the span
        attrs["http.flavor"] = f"{request.version.major}.{request.version.minor}"
        attrs["http.method"] = request.method
        attrs["http.request_content_length"] = request.content_length
        attrs["http.scheme"] = request.scheme
        attrs["http.target"] = request.path
        attrs["http.url"] = str(request.url).split("?", 1)[0]
        attrs["net.host.name"] = request.host
        socket_type = request.transport.get_extra_info("socket").type
        # this server only listen to TCP over unix socket or IP socket
        attrs["net.transport"] = (
            "ip_tcp" if socket_type == socket.SocketKind.SOCK_STREAM else "unix_tcp"
        )
        attrs["http.user_agent"] = request.headers.get("User-Agent", "")
        attrs["http.client_ip"] = request.headers.get("X-Forwarded-For", "")
        attrs["http.route"] = (
            request.match_info.route.resource.canonical
            if request.match_info.route.resource
            else ""
        )
    # set the attributes
    kwargs["attributes"] = attrs
    with tracer.start_as_current_span(*args, **kwargs) as span:
        yield span


def backoff_retry(i):
    return 0.1 * i + random.random() * (3.0 + 0.3 * i)


async def exec_script(request: web.Request, config) -> web.Response:
    async with start_as_current_span_async(
        "exec-script",
        kind=trace.SpanKind.INTERNAL,
    ) as parent:
        parent.set_attribute(
            "scripting_server.exec", config.get("execfile", config.get("execurl", ""))
        )
        # load script content
        async with start_as_current_span_async(
            "load",
            kind=trace.SpanKind.INTERNAL,
        ):
            exec_main_func = None
            execfile = config.get("execfile")
            if execfile:
                exec_cache_key = f"exec:{execfile}"
                if not DEBUG_MODE:
                    exec_main_func = await exec_cache.get(exec_cache_key)
                if not exec_main_func:
                    async with start_as_current_span_async(
                        "read-execfile",
                        kind=trace.SpanKind.INTERNAL,
                    ):
                        with open(os.path.join(BASE_SCRIPTS_PATH, execfile), "r") as f:
                            exec_content = f.read()
                    # load script with exec and extract the main function
                    exec_main_func = await exec_cache.execute_get_main(
                        exec_cache_key, exec_content
                    )
            # Execute script from an URL (execurl), only if execfile is not provided
            # The execute from URL feature is required only if want to
            # add/modify existing python script on the fly without deployment
            # via the Hasura Console. For security reason, this feature is disabled by default
            # recommend to use /upload endpoint to upload the new/modified script file instead.
            execurl = config.get("execurl")
            if ENGINE_PLUS_ALLOW_EXECURL:
                if execurl and not execfile:
                    # this is to increase security to prevent unauthorized script execution from URL
                    req_execute_secret = request.headers.get(
                        "X-Engine-Plus-Execute-Secret"
                    )
                    if not req_execute_secret:
                        raise ValueError(
                            "The header X-Engine-Plus-Execute-Secret is required in request to execute script from URL"
                        )
                    if req_execute_secret != ENGINE_PLUS_EXECUTE_SECRET:
                        raise ValueError(
                            "The value of header X-Engine-Plus-Execute-Secret is not matched the value of ENGINE_PLUS_EXECUTE_SECRET"
                        )
                    exec_cache_key = f"exec:{execurl}"
                    if not DEBUG_MODE:
                        exec_main_func = await exec_cache.get(exec_cache_key)
                    if not exec_main_func:
                        async with start_as_current_span_async(
                            "fetch-execurl",
                            kind=trace.SpanKind.INTERNAL,
                        ):
                            async with aiohttp.ClientSession() as http_session:
                                async with http_session.get(
                                    execurl, headers={"Cache-Control": "no-cache"}
                                ) as resp:
                                    exec_content = await resp.text()
                        # load script with exec and extract the main function
                        exec_main_func = await exec_cache.execute_get_main(
                            exec_cache_key, exec_content
                        )
            else:
                if execurl:
                    raise ValueError(
                        "To execute script from URL, you must set value for these environment: ENGINE_PLUS_ALLOW_EXECURL, ENGINE_PLUS_EXECUTE_SECRET"
                    )
        # exec_main_func must be loaded before this line so
        # it can be executed here in curent context
        if not exec_main_func:
            raise ValueError(
                "At least one of these headers must be specified: X-Engine-Plus-Execute-File, X-Engine-Plus-Execute-Url"
            )
        # execution of the script
        # The script must define this function: `async def main(request, env):`
        async with start_as_current_span_async(
            "run",
            kind=trace.SpanKind.INTERNAL,
        ):
            try:
                task = asyncio.get_running_loop().create_task(
                    exec_main_func(request, config["env"])
                )
            except TypeError:
                raise ValueError(
                    "The script must define this function: `async def main(request, env):`"
                )
            # schedule to run the task and wait for result.
            return await task


async def execute_code_handler(request: web.Request):
    # And execute the Python code.
    # mark starting time
    request.start_time = time.time()
    config = {
        "execfile": request.headers.get("X-Engine-Plus-Execute-File"),
        "execurl": request.headers.get("X-Engine-Plus-Execute-Url"),
        "env": {
            "json_encoder": request.app.get("json_encoder"),
            "json_decoder": request.app.get("json_decoder"),
            "exec_cache": request.app.get("exec_cache"),
            "redis_cluster": request.app.get("redis_cluster"),
            "redis_client": request.app.get("redis_client"),
            "redis_cluster_reader": request.app.get("redis_cluster_reader"),
            "redis_client_reader": request.app.get("redis_client_reader"),
            "graphql_client": request.app.get("graphql_client"),
            "boto3_session": request.app.get("boto3_session"),
            "boto3_context_stack": request.app.get("boto3_context_stack"),
            "boto3_dynamodb": request.app.get("boto3_dynamodb"),
            "boto3_s3": request.app.get("boto3_s3"),
            "boto3_sqs": request.app.get("boto3_sqs"),
        },
    }
    async with start_as_current_span_async(
        "scripting-server: /execute",
        kind=trace.SpanKind.INTERNAL,
        request=request,
    ) as parent:
        try:
            ### execute the python script using the configuration
            result = await exec_script(request, config)
            # check if result is a web.Response
            if isinstance(result, web.Response):
                # calculate the value for X-Execution-Time header
                x_execution_time = f"{time.time() - request.start_time}"
                result.headers["X-Execution-Time"] = x_execution_time
                parent.set_attribute("http.response.status_code", result.status)
                return result
            # assume the result is the JSON response and status code is 200
            status_code = 200
        except Exception as e:
            status_code = 400  # Hasura is expect status code to be either 200 or 400
            if isinstance(e, web.HTTPException):
                if e.status_code > 0:
                    parent.set_attribute("http.response.status_code", e.status_code)
                raise e
            elif isinstance(e, (SyntaxError, ValueError)):
                # response with format understandable by Hasura
                result = {
                    "message": str(e.__class__.__name__)
                    + ": "
                    + str(getattr(e, "msg", e.args[0] if len(e.args) else e)),
                    "extensions": {
                        "code": str(e.__class__.__name__),
                    },
                }
            else:
                parent.record_exception(e)
                # response with format understandable by Hasura
                result = {
                    "message": str(e.__class__.__name__)
                    + ": "
                    + str(getattr(e, "msg", e.args[0] if len(e.args) else e)),
                    "extensions": {
                        "code": str(e.__class__.__name__),
                        "traceback": str(traceback.format_exc()),
                    },
                }
                # log the error response
                logger.error(result)
        # finish the span
        if status_code >= 500:
            parent.set_status(trace.Status(trace.StatusCode.ERROR))
        parent.set_attribute("http.response.status_code", status_code)
        response_body = json_encoder.encode(result)
        # calculate the value for X-Execution-Time header
        x_execution_time = f"{time.time() - request.start_time}"
        # Return the result of the Python code execution.
        return web.Response(
            status=status_code,
            headers={
                "Content-type": "application/json",
                "X-Execution-Time": x_execution_time,
            },
            body=response_body,
        )


async def validate_json_code_handler(request: web.Request):
    # mark starting time
    request.start_time = time.time()
    config = {
        "execfile": request.headers.get("X-Engine-Plus-Execute-File"),
        "execurl": request.headers.get("X-Engine-Plus-Execute-Url"),
        "env": {
            # only these dependencies are allowed to be used for validation
            "json_encoder": request.app.get("json_encoder"),
            "json_decoder": request.app.get("json_decoder"),
            "exec_cache": request.app.get("exec_cache"),
            "graphql_client": request.app.get("graphql_client"),
        },
    }
    async with start_as_current_span_async(
        "scripting-server: /validate",
        kind=trace.SpanKind.INTERNAL,
        request=request,
    ) as parent:
        try:
            ### execute the python script with the configuration
            result = await exec_script(request, config)
            status_code = 200
        except Exception as e:
            status_code = 400  # Hasura is expect status code to be either 200 or 400
            if isinstance(e, web.HTTPException):
                if e.status_code > 0:
                    parent.set_attribute("http.response.status_code", e.status_code)
                raise e
            elif isinstance(e, (SyntaxError, ValueError, msgspec.ValidationError)):
                pass
            else:
                parent.record_exception(e)
            # response with format understandable by Hasura
            result = {
                "message": str(e.__class__.__name__)
                + ": "
                + str(getattr(e, "msg", e.args[0] if len(e.args) else e)),
            }
        # finish the span
        if status_code >= 500:
            parent.set_status(trace.Status(trace.StatusCode.ERROR))
        parent.set_attribute("http.response.status_code", status_code)
        # Return the result of the Python code execution.
        if status_code == 200:
            return web.Response(
                status=status_code,
                headers={
                    "Content-type": "text/plain",
                    "X-Execution-Time": f"{time.time() - request.start_time}",
                },
                body="OK",
            )
        response_body = json_encoder.encode(result)
        return web.Response(
            status=status_code,
            headers={
                "Content-type": "application/json",
                "X-Execution-Time": f"{time.time() - request.start_time}",
            },
            body=response_body,
        )


def exec_and_verify_script(exec_content: str):
    try:
        exec(exec_content)
        exec_main_func = locals()["main"]
    except Exception as e:
        if isinstance(e, KeyError) and "main" in str(e):
            raise ValueError(
                "The script must define this function `async def main(request: aiohttp.web_request.Request, env):`"
            )
        if isinstance(e, SyntaxError):
            raise ValueError(
                f"The script has SyntaxError at loc <{e.lineno}:{e.offset}>"
            )
        else:
            raise e
    if not inspect.iscoroutinefunction(exec_main_func):
        raise ValueError(
            "The main function must be an async function `async def main(request: aiohttp.web_request.Request, env):"
        )
    signature = inspect.signature(exec_main_func)
    if str(signature) not in {
        "(request: aiohttp.web_request.Request, env)",
        "(request: aiohttp.web_request.Request, env) -> aiohttp.web_response.Response",
    }:
        logger.error(
            f"Invalid signature of main function when upload script: {signature}"
        )
        raise ValueError(
            "The main function must be defined with this signature `async def main(request: aiohttp.web_request.Request, env) -> aiohttp.web_response.Response:"
        )
    return exec_main_func


async def upload_script_handler(request: web.Request):
    async with start_as_current_span_async(
        "scripting-server: /upload",
        kind=trace.SpanKind.INTERNAL,
        request=request,
    ) as parent:
        # read file content from request body
        data = await request.post()
        upload_path = data.get("path", "")
        is_library = data.get("is_library")
        upload_file_list = data.getall("file")
        if len(upload_file_list) == 0:
            return web.Response(
                status=400,
                headers={
                    "Content-type": "application/json",
                },
                body=json_encoder.encode({"message": "At least 1 `file` is required"}),
            )
        result = {"success": [], "message": ""}
        status_code = 200
        for script_file in upload_file_list:
            if not hasattr(script_file, "file"):
                logger.error(f"Invalid file field", extra={"file": script_file})
                continue
            exec_content = script_file.file.read().decode("utf-8")
            try:
                if not is_library:
                    async with start_as_current_span_async(
                        "validate-script",
                        kind=trace.SpanKind.INTERNAL,
                        attributes={
                            "script_file.filename": script_file.filename,
                        },
                    ):
                        exec_main_func = exec_and_verify_script(exec_content)
                else:  # the script is a library, hence we can set the exec_main_func to None
                    exec_main_func = None
                async with start_as_current_span_async(
                    "save-script",
                    kind=trace.SpanKind.INTERNAL,
                    attributes={
                        "upload_path": upload_path,
                        "is_library": bool(is_library),
                        "script_file.filename": script_file.filename,
                    },
                ):
                    # the script is seem to be a valid python script
                    # we can save it to cache and file system
                    if len(upload_path):
                        if upload_path.endswith(script_file.filename):
                            # fix the upload path if it contain file name
                            file_path = upload_path
                            upload_path = file_path.rsplit("/", 1)[0]
                        else:
                            file_path = os.path.join(upload_path, script_file.filename)
                    else:
                        file_path = script_file.filename
                    if is_library:
                        exec_cache_key = f"lib:{file_path}"
                    else:
                        exec_cache_key = f"exec:{file_path}"
                    exec_cache.pop(exec_cache_key, None)
                    # if the script is a library, exec_main_func will be None
                    # while the exec_content will be saved correctly
                    await exec_cache.set(exec_cache_key, exec_main_func, exec_content)
                    # also write the script to local file system
                    if len(upload_path):
                        # we need to create the folder if not exists
                        os.makedirs(
                            os.path.join(BASE_SCRIPTS_PATH, upload_path), exist_ok=True
                        )
                    with open(
                        os.path.join(BASE_SCRIPTS_PATH, file_path),
                        "w",
                    ) as f:
                        f.write(exec_content)
                result["success"].append(script_file.filename)
            except Exception as e:
                if isinstance(e, web.HTTPException):
                    if e.status_code > 0:
                        parent.set_attribute("http.response.status_code", e.status_code)
                    raise e
                if isinstance(e, ValueError):
                    status_code = 400
                    result[
                        "message"
                    ] += f"Error in {script_file.filename}: {e.args[0]},"
                else:
                    parent.record_exception(e)
                    status_code = 500
                    result = {
                        "error": str(e.__class__.__name__),
                        "message": str(e.__class__.__name__)
                        + ": "
                        + str(getattr(e, "msg", e.args[0] if len(e.args) else e)),
                    }
                    break
        # result
        return web.Response(
            status=status_code,
            headers={
                "Content-type": "application/json",
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
                os.getenv("HASURA_GRAPHQL_READ_REPLICA_URLS")
                and "replica" not in not_include
            ):
                async with http_session.get(
                    "http://localhost:8882/healthz", timeout=5
                ) as resp:
                    # extract the response status code and body
                    result["replica"] = {
                        "status": resp.status,
                        "body": await resp.text(),
                    }
                    if resp.status == 200:
                        # replica is available, set the flag in graphql_client to True
                        request.app["graphql_client"]._client_ro_available = True
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
    redis_cluster_url = os.getenv("HASURA_GRAPHQL_REDIS_CLUSTER_URL")
    redis_url = os.getenv("HASURA_GRAPHQL_REDIS_URL")
    redis_reader_url = os.getenv("HASURA_GRAPHQL_REDIS_READER_URL")
    if redis_cluster_url:
        app["redis_client"] = app["redis_cluster"] = await redis.RedisCluster.from_url(
            redis_cluster_url
        )
        app["redis_client_reader"] = app["redis_cluster_reader"] = (
            await redis.RedisCluster.from_url(
                redis_cluster_url, read_from_replicas=True
            )
        )
        exec_cache.set_redis_instance(app["redis_client"])
    if redis_url:
        app["redis_client"] = await redis.from_url(redis_url)
        exec_cache.set_redis_instance(app["redis_client"])
    if redis_reader_url:
        app["redis_client_reader"] = await redis.from_url(redis_reader_url)

    if ENABLE_OTEL and (redis_cluster_url or redis_url or redis_reader_url):
        from opentelemetry.instrumentation.redis import RedisInstrumentor

        RedisInstrumentor().instrument()
    app["graphql_client"] = GqlAsyncClient(tracer)
    app["json_encoder"] = json_encoder
    app["json_decoder"] = json_decoder
    # override the default aiohttp typedef JSONEncoder, JSONDecoder
    aiohttp.typedefs.DEFAULT_JSON_ENCODER = json_encoder.encode
    aiohttp.typedefs.DEFAULT_JSON_DECODER = json_decoder.decode

    app["exec_cache"] = exec_cache

    # init boto3 session if enabled, this allow faster boto3 connection in scripts
    if ENGINE_PLUS_ENABLE_BOTO3:
        init_resources = ENGINE_PLUS_ENABLE_BOTO3.split(",")
        context_stack = contextlib.AsyncExitStack()
        app["boto3_context_stack"] = context_stack
        app["boto3_session"] = boto3_session
        if "dynamodb" in init_resources:
            app["boto3_dynamodb"] = await context_stack.enter_async_context(
                boto3_session.resource(
                    "dynamodb",
                    endpoint_url=os.getenv("AWS_BOTO3_DYNAMODB_ENDPOINT_URL"),
                )
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
    # add validate scripting endpoint
    app.router.add_post("/validate", validate_json_code_handler)
    # add upload script endpoint
    app.router.add_post("/upload", upload_script_handler)

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
    disable_loggers = [
        "boto",
        "urllib3",
        "s3transfer",
        "boto3",
        "botocore",
        "aioboto3",
        "aiobotocore",
        "gql.transport.aiohttp",
    ]
    # set log level to ERROR for these loggers to reduce log noise
    for name in disable_loggers:
        logging.getLogger(name).setLevel(logging.ERROR)
    return app


async def cleanup_server(app):
    futures = []
    # close the redis connections
    redis_cluster = app.get("redis_cluster")
    if redis_cluster:
        print("Scripting server shutdown: Closing redis-cluster connection")
        futures.append(redis_cluster.aclose())
    redis_cluster_reader = app.get("redis_cluster_reader")
    if redis_cluster_reader:
        print("Scripting server shutdown: Closing redis-cluster-reader connection")
        futures.append(redis_cluster_reader.aclose())
    redis_client = app.get("redis_client")
    if redis_client:
        print("Scripting server shutdown: Closing redis connection")
        futures.append(redis_client.aclose())
    redis_client_reader = app.get("redis_client_reader")
    if redis_client_reader:
        print("Scripting server shutdown: Closing redis-reader connection")
        futures.append(redis_client_reader.aclose())
    # close boto3 session
    boto3_context_stack = app.get("boto3_context_stack")
    if boto3_context_stack:
        print("Scripting server shutdown: Closing boto3 session")
        futures.append(boto3_context_stack.aclose())
    # wait all futures
    await asyncio.gather(*futures)
    print("Scripting server shutdown: Finished")


class AccessLogger(AbstractAccessLogger):
    def get_trace_id(self, request):
        trace_id = request.headers.get("Traceparent", "")
        if not trace_id:
            trace_id = request.headers.get("X-Amzn-Trace-Id", "")
        if not trace_id:
            trace_id = request.headers.get("X-B3-TraceId", "")
        return trace_id

    def get_request_id(self, request):
        request_id = request.headers.get("X-Request-ID", "")
        if not request_id:
            request_id = request.headers.get("x-request-id", "")
        return request_id

    def log(self, request, response, response_time):
        if getattr(request, "silent_access_log", False):
            return
        latency = float(response.headers.get("X-Execution-Time", response_time))
        # start time of request in UNIX time %t
        start_time = getattr(request, "start_time", time.time() - latency)
        self.logger.info(
            f'{datetime.fromtimestamp(start_time).strftime("%Y-%m-%dT%H:%M:%S.%f")}'
            f' " {request.method.ljust(8)} {request.path}" {response.status}  {latency*1000:6f}ms'
            f' ({response.body_length}) [{self.get_request_id(request)};{self.get_trace_id(request)}] "{request.headers.get("Referer", "")}" "{request.headers.get("User-Agent", "")}"'
        )


parser = argparse.ArgumentParser(description="aiohttp scripting-server")
parser.add_argument("--path")
parser.add_argument("--port", type=int)

if __name__ == "__main__":
    asyncio.set_event_loop_policy(uvloop.EventLoopPolicy())
    args = parser.parse_args()
    web.run_app(
        get_app(),
        path=args.path,
        port=args.port,
        access_log_class=AccessLogger,
    )
    # try to cancel any running async tasks
    for task in async_tasks:
        if not task.done():
            task.cancel()
    print("Scripting server is gracefully shutdown")
    sys.exit(0)
