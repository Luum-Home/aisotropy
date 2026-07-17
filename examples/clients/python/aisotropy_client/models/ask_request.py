from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset






T = TypeVar("T", bound="AskRequest")



@_attrs_define
class AskRequest:
    """ 
        Attributes:
            product_id (str): Product or module ID (selects the persona base prompt)
            prompt (str): Question or instruction to answer statelessly
            schema (str | Unset): A URL to the JSON Schema for this object.
     """

    product_id: str
    prompt: str
    schema: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        product_id = self.product_id

        prompt = self.prompt

        schema = self.schema


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "product_id": product_id,
            "prompt": prompt,
        })
        if schema is not UNSET:
            field_dict["$schema"] = schema

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        d = dict(src_dict)
        product_id = d.pop("product_id")

        prompt = d.pop("prompt")

        schema = d.pop("$schema", UNSET)

        ask_request = cls(
            product_id=product_id,
            prompt=prompt,
            schema=schema,
        )

        return ask_request

