from aiohttp import web
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from gql import Client
    from msgspec.json import Encoder, Decoder
    from typing import List

# do not import here unless for type hinting, must import in main() function


async def main(request: web.Request, env):
    # import logging
    # logger = logging.getLogger("quick_validate.py")
    # validate the input data
    import msgspec
    
    should_reload = await env["exec_cache"].load_lib("libs/lib_types.py")
    from libs import lib_types
    if should_reload:
        from importlib import reload
        reload(lib_types)

    # read body using custom decoder
    payload = lib_types.insert_customer_validator.decode(await request.text())
    for c in payload.data.input:
        if c.id is not None:
            raise msgspec.ValidationError("id must not be specified when insert")
        if c.created_at is not None:
            raise msgspec.ValidationError(
                "created_at must not be specified when insert"
            )
        if c.updated_at is not None:
            raise msgspec.ValidationError(
                "updated_at must not be specified when insert"
            )
        if c.first_name == c.last_name:
            raise msgspec.ValidationError("first_name is the same as last_name")
    # you can do more validation using graphql_client or redis_client
    # from gql import gql
    # graphql_client: Client = env["graphql_client"]
    # query = gql(
    #     """query MyQuery {
    #     customer(limit: 1, where: {created_at: {_gt: "2023-08-01", _lt: "2028-09-11"}}) {
    #       id
    #       last_name
    #       first_name
    #     }
    #   }"""
    # )
    # query_result = await graphql_client.execute(query)
