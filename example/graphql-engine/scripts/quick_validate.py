from aiohttp import web

# do not import here, must import in main() function


async def main(request: web.Request, body):
    # import logging
    # logger = logging.getLogger("quick_validate.py")
    # validate the input data
    import msgspec
    from typing import Annotated, Optional, List
    from datetime import date, datetime
    from gql import gql, Client

    class InsertCustomer(msgspec.Struct):
        first_name: str
        last_name: str
        external_ref_list: List[str]
        id: Optional[str] = None
        date_of_birth: Optional[date] = None
        gender: Annotated[int, msgspec.Meta(ge=0, le=9)] = None
        created_at: Optional[datetime] = None
        updated_at: Optional[datetime] = None

    for c in msgspec.convert(body.data.input, type=List[InsertCustomer]):
        if c.id is not None:
            raise ValueError("id must not be specified when insert")
        if c.created_at is not None:
            raise ValueError("created_at must not be specified when insert")
        if c.updated_at is not None:
            raise ValueError("updated_at must not be specified when insert")
        if c.first_name == c.last_name:
            raise ValueError("first_name is the same as last_name")
    # you can do more validation using graphql_client or redis_client
    graphql_client: Client = request.app["graphql_client"]
    query = gql(
        """query MyQuery {
        customer(limit: 1, where: {created_at: {_gt: "2023-08-01", _lt: "2023-09-11"}}) {
          id
          last_name
          first_name
        }
      }"""
    )
    result = await graphql_client.execute(query)
