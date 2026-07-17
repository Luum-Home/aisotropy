from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset






T = TypeVar("T", bound="AisdkUIPart")



@_attrs_define
class AisdkUIPart:
    """ 
        Attributes:
            type_ (str): Part discriminator; only "text" contributes to the decoded prompt
            text (str | Unset): Text content when Type == "text"
     """

    type_: str
    text: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        type_ = self.type_

        text = self.text


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "type": type_,
        })
        if text is not UNSET:
            field_dict["text"] = text

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        d = dict(src_dict)
        type_ = d.pop("type")

        text = d.pop("text", UNSET)

        aisdk_ui_part = cls(
            type_=type_,
            text=text,
        )

        return aisdk_ui_part

