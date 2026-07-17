from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset







T = TypeVar("T", bound="StreamErrorEvent")



@_attrs_define
class StreamErrorEvent:
    """ 
        Attributes:
            message (str): Error description
     """

    message: str





    def to_dict(self) -> dict[str, Any]:
        message = self.message


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "message": message,
        })

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        d = dict(src_dict)
        message = d.pop("message")

        stream_error_event = cls(
            message=message,
        )

        return stream_error_event

