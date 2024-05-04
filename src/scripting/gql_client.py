import os
import random
from gql.transport.aiohttp import AIOHTTPTransport, TransportAlreadyConnected
from graphql import DocumentNode, ExecutionResult
from gql import Client
from typing import Any, AsyncGenerator, Dict, Optional, Union
import re
from opentelemetry import trace
import logging

logger = logging.getLogger("scripting-gql-client")
server_host = os.getenv("ENGINE_PLUS_SERVER_HOST", "localhost")
server_port = os.getenv("ENGINE_PLUS_SERVER_PORT", "8000")
primary_weight_vs_replica = int(
    os.getenv("ENGINE_PLUS_GRAPHQL_PRIMARY_VS_REPLICA_WEIGHT", 50)
)
# graphql_v1path = os.getenv("ENGINE_PLUS_GRAPHQL_V1_PATH", "/public/graphql/v1")
hasura_admin_secret = os.environ["HASURA_GRAPHQL_ADMIN_SECRET"]
go_graphql_ropath = os.getenv(
    "ENGINE_PLUS_GRAPHQL_V1_READONLY_PATH", "/public/graphql/v1readonly"
)
get_v1_http_transport = lambda: AIOHTTPTransport(
    url=f"http://localhost:8881/v1/graphql",
    headers={"x-hasura-admin-secret": hasura_admin_secret},
)
get_ro_http_transport = lambda: AIOHTTPTransport(
    url=f"http://localhost:8882/v1/graphql",
    headers={"x-hasura-admin-secret": hasura_admin_secret},
)
get_go_http_transport = lambda: AIOHTTPTransport(
    url=f"http://{server_host}:{server_port}{go_graphql_ropath}",
    headers={"x-hasura-admin-secret": hasura_admin_secret},
)

mutationRegex = re.compile("^\s*mutation\W", re.IGNORECASE)
cachedQueryRegex = re.compile("^\s*query\W+\w*.*\s*@cached", re.IGNORECASE)


def is_mutation(query: str) -> bool:
    return mutationRegex.match(query) is not None


def is_cached_query(query: str) -> bool:
    return cachedQueryRegex.match(query) is not None


class GqlAsyncClient:
    timeout = 60
    max_retries = 3

    def __init__(self, tracer: trace.Tracer):
        self._client = Client(
            transport=get_v1_http_transport(), execute_timeout=self.timeout
        )
        self._client_ro = Client(
            transport=get_ro_http_transport(), execute_timeout=self.timeout
        )
        self._client_go = Client(
            transport=get_go_http_transport(), execute_timeout=self.timeout
        )
        self.tracer = tracer

    async def validate(self, document: DocumentNode):
        if self._client.schema is None:
            # fetch schema on demand when the first time validate is called
            await self._client.session.fetch_schema()

        return self._client.validate(document)

    async def execute(
        self,
        document: DocumentNode,
        variable_values: Optional[Dict[str, Any]] = None,
        operation_name: Optional[str] = None,
        serialize_variables: Optional[bool] = None,
        parse_result: Optional[bool] = None,
        get_execution_result: bool = False,
        **kwargs,
    ) -> Union[Dict[str, Any], ExecutionResult]:
        with self.tracer.start_as_current_span(
            "GQLAsyncClient.execute",
            kind=trace.SpanKind.INTERNAL,
        ) as span:
            # check if the document is a mutation
            # if it is, then use the primary
            # else use the replica
            gqlQueryString = document.loc.source.body
            span.set_attribute("graphql.document", gqlQueryString)
            span.set_attribute("graphql.operation.name", operation_name if operation_name else "")
            if is_mutation(gqlQueryString):
                span.set_attribute("graphql.operation.type", "mutation")
                client: Client = self._client
                get_transport_func = get_v1_http_transport
            elif is_cached_query(gqlQueryString):
                span.set_attribute("graphql.operation.type", "query")
                client = self._client_go
                get_transport_func = get_go_http_transport
            else:
                span.set_attribute("graphql.operation.type", "query")
                weight = primary_weight_vs_replica
                # check if the replica engine is available
                if not getattr(self, "_client_ro_available", False):
                    # its not available, we need to
                    # set weight to 100 to force using primary.
                    weight = 100
                if weight >= 100 or random.randint(0, 100) < weight:
                    client = self._client
                    get_transport_func = get_v1_http_transport
                else:
                    client = self._client_ro
                    get_transport_func = get_ro_http_transport
            span.set_attribute("http.url", client.transport.url)
            for i in range(self.max_retries):
                try:
                    async with client as gql_client:
                        return await gql_client.execute(
                            document,
                            variable_values=variable_values,
                            operation_name=operation_name,
                            serialize_variables=serialize_variables,
                            parse_result=parse_result,
                            get_execution_result=get_execution_result,
                            **kwargs,
                        )
                except TransportAlreadyConnected:
                    client = Client(
                        transport=get_transport_func(), execute_timeout=self.timeout
                    )

    async def subscribe(
        self,
        document: DocumentNode,
        variable_values: Optional[Dict[str, Any]] = None,
        operation_name: Optional[str] = None,
        serialize_variables: Optional[bool] = None,
        parse_result: Optional[bool] = None,
        get_execution_result: bool = False,
        **kwargs,
    ) -> Union[
        AsyncGenerator[Dict[str, Any], None], AsyncGenerator[ExecutionResult, None]
    ]:
        with self.tracer.start_as_current_span(
            "GQLAsyncClient.subscribe",
            kind=trace.SpanKind.INTERNAL,
        ) as span:
            gqlQueryString = document.loc.source.body
            span.set_attribute("graphql.document", gqlQueryString)
            span.set_attribute("graphql.operation.name", operation_name if operation_name else "")
            span.set_attribute("graphql.operation.type", "subscription")
            span.set_attribute("http.url", self._client.transport.url)
            # TODO: load balance between primary and replica
            async with self._client as gql_client:
                return await gql_client.subscribe(
                    document,
                    variable_values=variable_values,
                    operation_name=operation_name,
                    serialize_variables=serialize_variables,
                    parse_result=parse_result,
                    get_execution_result=get_execution_result,
                    **kwargs,
                )
