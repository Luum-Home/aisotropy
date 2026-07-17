from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset






T = TypeVar("T", bound="KnowledgeDeleteOutputBody")



@_attrs_define
class KnowledgeDeleteOutputBody:
    """ 
        Attributes:
            deleted (int): Number of KindDocument chunks removed
            schema (str | Unset): A URL to the JSON Schema for this object.
     """

    deleted: int
    schema: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        deleted = self.deleted

        schema = self.schema


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "deleted": deleted,
        })
        if schema is not UNSET:
            field_dict["$schema"] = schema

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        d = dict(src_dict)
        deleted = d.pop("deleted")

        schema = d.pop("$schema", UNSET)

        knowledge_delete_output_body = cls(
            deleted=deleted,
            schema=schema,
        )

        return knowledge_delete_output_body

