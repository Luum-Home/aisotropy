from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset
from typing import cast

if TYPE_CHECKING:
  from ..models.knowledge_document import KnowledgeDocument





T = TypeVar("T", bound="KnowledgeRequest")



@_attrs_define
class KnowledgeRequest:
    """ 
        Attributes:
            documents (list[KnowledgeDocument] | None): Documents to chunk, embed, and store as KindDocument vectors
            schema (str | Unset): A URL to the JSON Schema for this object.
     """

    documents: list[KnowledgeDocument] | None
    schema: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        from ..models.knowledge_document import KnowledgeDocument
        documents: list[dict[str, Any]] | None
        if isinstance(self.documents, list):
            documents = []
            for documents_type_0_item_data in self.documents:
                documents_type_0_item = documents_type_0_item_data.to_dict()
                documents.append(documents_type_0_item)


        else:
            documents = self.documents

        schema = self.schema


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "documents": documents,
        })
        if schema is not UNSET:
            field_dict["$schema"] = schema

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        from ..models.knowledge_document import KnowledgeDocument
        d = dict(src_dict)
        def _parse_documents(data: object) -> list[KnowledgeDocument] | None:
            if data is None:
                return data
            try:
                if not isinstance(data, list):
                    raise TypeError()
                documents_type_0 = []
                _documents_type_0 = data
                for documents_type_0_item_data in (_documents_type_0):
                    documents_type_0_item = KnowledgeDocument.from_dict(documents_type_0_item_data)



                    documents_type_0.append(documents_type_0_item)

                return documents_type_0
            except (TypeError, ValueError, AttributeError, KeyError):
                pass
            return cast(list[KnowledgeDocument] | None, data)

        documents = _parse_documents(d.pop("documents"))


        schema = d.pop("$schema", UNSET)

        knowledge_request = cls(
            documents=documents,
            schema=schema,
        )

        return knowledge_request

