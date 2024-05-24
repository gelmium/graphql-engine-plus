from aiohttp import web
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from gql import Client

# do not import here unless for type hinting, must import in main() function


async def main(request: web.Request, body):
    # import logging
    # logger = logging.getLogger("quick_validate.py")
    # validate the input data
    import msgspec
    from typing import List

    try:
        from lib_types import InsertCustomer
    except ImportError:
        await request.app["exec_cache"].load_lib("lib_types.py")
        from lib_types import InsertCustomer

    for c in msgspec.convert(body.data.input, type=List[InsertCustomer]):
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
    # graphql_client: Client = request.app["graphql_client"]
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
