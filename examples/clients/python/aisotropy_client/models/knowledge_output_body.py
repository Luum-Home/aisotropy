from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset






T = TypeVar("T", bound="KnowledgeOutputBody")



@_attrs_define
class KnowledgeOutputBody:
    """ 
        Attributes:
            chunks_ingested (int): Number of KindDocument chunks written across all submitted documents
            schema (str | Unset): A URL to the JSON Schema for this object.
     """

    chunks_ingested: int
    schema: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        chunks_ingested = self.chunks_ingested

        schema = self.schema


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "chunks_ingested": chunks_ingested,
        })
        if schema is not UNSET:
            field_dict["$schema"] = schema

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        d = dict(src_dict)
        chunks_ingested = d.pop("chunks_ingested")

        schema = d.pop("$schema", UNSET)

        knowledge_output_body = cls(
            chunks_ingested=chunks_ingested,
            schema=schema,
        )

        return knowledge_output_body

