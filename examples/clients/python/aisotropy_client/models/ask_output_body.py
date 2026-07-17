from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset






T = TypeVar("T", bound="AskOutputBody")



@_attrs_define
class AskOutputBody:
    """ 
        Attributes:
            cached (bool): true when served from the response cache without an LLM call
            reply (str): The answer (freshly generated, or served from cache)
            stub (bool): true when the resolved LLM client is the credential-less stub
            tenant_id (str): Tenant that served the request (isolation echo)
            schema (str | Unset): A URL to the JSON Schema for this object.
     """

    cached: bool
    reply: str
    stub: bool
    tenant_id: str
    schema: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        cached = self.cached

        reply = self.reply

        stub = self.stub

        tenant_id = self.tenant_id

        schema = self.schema


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "cached": cached,
            "reply": reply,
            "stub": stub,
            "tenant_id": tenant_id,
        })
        if schema is not UNSET:
            field_dict["$schema"] = schema

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        d = dict(src_dict)
        cached = d.pop("cached")

        reply = d.pop("reply")

        stub = d.pop("stub")

        tenant_id = d.pop("tenant_id")

        schema = d.pop("$schema", UNSET)

        ask_output_body = cls(
            cached=cached,
            reply=reply,
            stub=stub,
            tenant_id=tenant_id,
            schema=schema,
        )

        return ask_output_body

