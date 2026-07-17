from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset






T = TypeVar("T", bound="ChatOutputBody")



@_attrs_define
class ChatOutputBody:
    """ 
        Attributes:
            reply (str): Agent response, schema-enforced via the reflection loop
            stub (bool): true when the resolved LLM client is the credential-less stub (no provider key configured)
            tenant_id (str): Tenant that served the request (isolation echo)
            schema (str | Unset): A URL to the JSON Schema for this object.
     """

    reply: str
    stub: bool
    tenant_id: str
    schema: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        reply = self.reply

        stub = self.stub

        tenant_id = self.tenant_id

        schema = self.schema


        field_dict: dict[str, Any] = {}

        field_dict.update({
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
        reply = d.pop("reply")

        stub = d.pop("stub")

        tenant_id = d.pop("tenant_id")

        schema = d.pop("$schema", UNSET)

        chat_output_body = cls(
            reply=reply,
            stub=stub,
            tenant_id=tenant_id,
            schema=schema,
        )

        return chat_output_body

