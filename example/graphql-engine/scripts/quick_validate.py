from aiohttp import web

# do not import here, must import in main() function


async def main(request: web.Request, body):
    import logging
    from pydantic import BaseModel, conint
    from datetime import date, datetime
    from typing import Optional, List, Dict, Any, TypeVar, Generic
    from pydantic.generics import GenericModel

    T = TypeVar("T")

    class ValidationData(GenericModel, Generic[T]):
        input: List[T]

    class Payload(GenericModel, Generic[T]):
        data: ValidationData[T]
        role: str
        session_variables: Dict[str, Any]
        version: int

    class InsertCustomer(BaseModel):
        id: Optional[str] = None
        first_name: str
        last_name: str
        external_ref_list: List[str]
        date_of_birth: Optional[date] = None
        gender: Optional[conint(ge=0, le=9)] = None
        created_at: Optional[datetime] = None
        updated_at: Optional[datetime] = None

    logger = logging.getLogger("quick_validate.py")
    # required params from body
    payload = Payload[InsertCustomer](**body["payload"])
    # logger.debug(f"payload: {payload}")
    # validate the input data
    for c in payload.data.input:
        if c.id is not None:
            raise ValueError(f"id {c.id} must not be specified when insert")
        if c.created_at is not None:
            raise ValueError(
                f"created_at {c.created_at} must not be specified when insert"
            )
        if c.updated_at is not None:
            raise ValueError(
                f"updated_at {c.updated_at} must not be specified when insert"
            )
        if c.first_name == c.last_name:
            raise ValueError(
                f"first_name {input['first_name']} is same as last_name {input['last_name']}"
            )
    # you can do more validation using database connection or redis cache
    body["payload"] = {}
