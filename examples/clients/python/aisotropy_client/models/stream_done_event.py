from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset







T = TypeVar("T", bound="StreamDoneEvent")



@_attrs_define
class StreamDoneEvent:
    """ 
        Attributes:
            tenant_id (str): Tenant that served the request
     """

    tenant_id: str





    def to_dict(self) -> dict[str, Any]:
        tenant_id = self.tenant_id


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "tenant_id": tenant_id,
        })

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        d = dict(src_dict)
        tenant_id = d.pop("tenant_id")

        stream_done_event = cls(
            tenant_id=tenant_id,
        )

        return stream_done_event

