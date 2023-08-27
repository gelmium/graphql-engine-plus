import os
from gql.transport.aiohttp import AIOHTTPTransport
from graphql import DocumentNode, ExecutionResult
from gql import Client
from typing import Any, AsyncGenerator, Dict, Optional, Literal, Union

server_host = os.environ.get("ENGINE_PLUS_SERVER_HOST", "localhost")
server_port = os.environ.get("ENGINE_PLUS_SERVER_PORT", "8000")
# graphql_v1path = os.environ.get("ENGINE_PLUS_GRAPHQL_V1_PATH", "/public/graphql/v1")
# graphql_v2path = os.environ.get("ENGINE_PLUS_GRAPHQL_V2_PATH", "/public/graphql/v2")
go_graphql_ropath = os.environ.get(
    "ENGINE_PLUS_GRAPHQL_V1_READONLY_PATH", "/public/graphql/v1readonly"
)
v1_http_transport = AIOHTTPTransport(
    url=f"http://localhost:8881/v1/graphql",
    headers={"x-hasura-admin-secret": os.environ["HASURA_GRAPHQL_ADMIN_SECRET"]},
)
v2_http_transport = AIOHTTPTransport(
    url=f"http://localhost:8882/v1/graphql",
    headers={"x-hasura-admin-secret": os.environ["HASURA_GRAPHQL_ADMIN_SECRET"]},
)
ro_http_transport = AIOHTTPTransport(
    url=f"http://localhost:8880/v1/graphql",
    headers={"x-hasura-admin-secret": os.environ["HASURA_GRAPHQL_ADMIN_SECRET"]},
)
go_http_transport = AIOHTTPTransport(
    url=f"http://{server_host}:{server_port}{go_graphql_ropath}",
    headers={"x-hasura-admin-secret": os.environ["HASURA_GRAPHQL_ADMIN_SECRET"]},
)


class GqlAsyncClient:
    def __init__(self):
        self._client = Client(transport=v1_http_transport, execute_timeout=60)
        self._client_v2 = Client(transport=v2_http_transport, execute_timeout=60)
        self._client_ro = Client(transport=ro_http_transport, execute_timeout=60)
        self._client_go = Client(transport=go_http_transport, execute_timeout=60)

    async def validate(self, document: DocumentNode):
        if self._client.schema is None:
            # fetch schema on demand when the first time validate is called
            await self._client.session.fetch_schema()

        return self._client.validate(document)

    async def validate_v2(self, document: DocumentNode):
        if self._client_v2.schema is None:
            # fetch schema on demand when the first time validate is called
            await self._client_v2.session.fetch_schema()

        return self._client_v2.validate(document)

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
        async with self._client as gql_client:
            return await gql_client.execute(
                document,
                variable_values=variable_values,
                operation_name=operation_name,
                serialize_variables=serialize_variables,
                parse_result=parse_result,
                get_execution_result=get_execution_result,
                **kwargs,
            )

    async def execute_v2(
        self,
        document: DocumentNode,
        variable_values: Optional[Dict[str, Any]] = None,
        operation_name: Optional[str] = None,
        serialize_variables: Optional[bool] = None,
        parse_result: Optional[bool] = None,
        get_execution_result: bool = False,
        **kwargs,
    ) -> Union[Dict[str, Any], ExecutionResult]:
        async with self._client_v2 as gql_client:
            return await gql_client.execute(
                document,
                variable_values=variable_values,
                operation_name=operation_name,
                serialize_variables=serialize_variables,
                parse_result=parse_result,
                get_execution_result=get_execution_result,
                **kwargs,
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

    async def subscribe_v2(
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
        async with self._client_v2 as gql_client:
            return await gql_client.subscribe(
                document,
                variable_values=variable_values,
                operation_name=operation_name,
                serialize_variables=serialize_variables,
                parse_result=parse_result,
                get_execution_result=get_execution_result,
                **kwargs,
            )

    ## Cache or Replica
    async def execute_v1_query_with_cache(
        self,
        document: DocumentNode,
        variable_values: Optional[Dict[str, Any]] = None,
        operation_name: Optional[str] = None,
        serialize_variables: Optional[bool] = None,
        parse_result: Optional[bool] = None,
        get_execution_result: bool = False,
        **kwargs,
    ) -> Union[Dict[str, Any], ExecutionResult]:
        async with self._client_go as gql_client:
            return await gql_client.execute(
                document,
                variable_values=variable_values,
                operation_name=operation_name,
                serialize_variables=serialize_variables,
                parse_result=parse_result,
                get_execution_result=get_execution_result,
                **kwargs,
            )

    async def subscribe_v1_replica(
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
        async with self._client_ro as gql_client:
            return await gql_client.subscribe(
                document,
                variable_values=variable_values,
                operation_name=operation_name,
                serialize_variables=serialize_variables,
                parse_result=parse_result,
                get_execution_result=get_execution_result,
                **kwargs,
            )
