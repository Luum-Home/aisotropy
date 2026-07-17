from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset






T = TypeVar("T", bound="KnowledgeDocument")



@_attrs_define
class KnowledgeDocument:
    """ 
        Attributes:
            content (str): Plain-text document body to chunk, embed, and store
            id (str): Caller-chosen document identifier; re-ingesting the same id idempotently replaces its chunks
            source (str | Unset): Optional provenance (url, filename, section) surfaced on retrieved chunks for citations
     """

    content: str
    id: str
    source: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        content = self.content

        id = self.id

        source = self.source


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "content": content,
            "id": id,
        })
        if source is not UNSET:
            field_dict["source"] = source

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        d = dict(src_dict)
        content = d.pop("content")

        id = d.pop("id")

        source = d.pop("source", UNSET)

        knowledge_document = cls(
            content=content,
            id=id,
            source=source,
        )

        return knowledge_document

