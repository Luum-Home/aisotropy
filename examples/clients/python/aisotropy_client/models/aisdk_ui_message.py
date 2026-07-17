from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset
from typing import cast

if TYPE_CHECKING:
  from ..models.aisdk_ui_part import AisdkUIPart





T = TypeVar("T", bound="AisdkUIMessage")



@_attrs_define
class AisdkUIMessage:
    """ 
        Attributes:
            parts (list[AisdkUIPart] | None): Ordered content parts of the message
            role (str): Message role (user/assistant/system)
            id (str | Unset): Client message id
     """

    parts: list[AisdkUIPart] | None
    role: str
    id: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        from ..models.aisdk_ui_part import AisdkUIPart
        parts: list[dict[str, Any]] | None
        if isinstance(self.parts, list):
            parts = []
            for parts_type_0_item_data in self.parts:
                parts_type_0_item = parts_type_0_item_data.to_dict()
                parts.append(parts_type_0_item)


        else:
            parts = self.parts

        role = self.role

        id = self.id


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "parts": parts,
            "role": role,
        })
        if id is not UNSET:
            field_dict["id"] = id

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        from ..models.aisdk_ui_part import AisdkUIPart
        d = dict(src_dict)
        def _parse_parts(data: object) -> list[AisdkUIPart] | None:
            if data is None:
                return data
            try:
                if not isinstance(data, list):
                    raise TypeError()
                parts_type_0 = []
                _parts_type_0 = data
                for parts_type_0_item_data in (_parts_type_0):
                    parts_type_0_item = AisdkUIPart.from_dict(parts_type_0_item_data)



                    parts_type_0.append(parts_type_0_item)

                return parts_type_0
            except (TypeError, ValueError, AttributeError, KeyError):
                pass
            return cast(list[AisdkUIPart] | None, data)

        parts = _parse_parts(d.pop("parts"))


        role = d.pop("role")

        id = d.pop("id", UNSET)

        aisdk_ui_message = cls(
            parts=parts,
            role=role,
            id=id,
        )

        return aisdk_ui_message

