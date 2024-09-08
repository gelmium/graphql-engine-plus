import msgspec
from typing import Annotated, Optional, List
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
