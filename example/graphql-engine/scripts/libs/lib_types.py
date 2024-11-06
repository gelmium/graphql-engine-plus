import msgspec
from typing import Annotated, Optional, List, Dict, TypeVar, Generic
from datetime import date, datetime


class InsertCustomer(msgspec.Struct):
    first_name: str
    last_name: str
    external_ref_list: List[str]
    id: Optional[str] = None
    date_of_birth: Optional[date] = None
    gender: Annotated[int, msgspec.Meta(ge=0, le=9)] = None
    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None


T = TypeVar("T")


class ValidationData(msgspec.Struct, Generic[T]):
    input: List[T]


class ValidatePayload(msgspec.Struct, Generic[T]):
    data: ValidationData[T]
    role: str
    session_variables: Dict[str, str]
    version: int


insert_customer_validator = msgspec.json.Decoder(ValidatePayload[InsertCustomer])
