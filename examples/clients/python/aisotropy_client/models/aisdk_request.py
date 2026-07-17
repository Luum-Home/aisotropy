from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset
from typing import cast

if TYPE_CHECKING:
  from ..models.aisdk_ui_message import AisdkUIMessage





T = TypeVar("T", bound="AisdkRequest")



@_attrs_define
class AisdkRequest:
    """ 
        Attributes:
            messages (list[AisdkUIMessage] | None): Conversation transcript; the latest user message's text becomes the
                prompt
            schema (str | Unset): A URL to the JSON Schema for this object.
            id (str | Unset): AI SDK chat/thread id; used as the working-memory session key when session_id is absent
            product_id (str | Unset): Optional persona/product selector
            session_id (str | Unset): Optional explicit working-memory session key (overrides id)
     """

    messages: list[AisdkUIMessage] | None
    schema: str | Unset = UNSET
    id: str | Unset = UNSET
    product_id: str | Unset = UNSET
    session_id: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        from ..models.aisdk_ui_message import AisdkUIMessage
        messages: list[dict[str, Any]] | None
        if isinstance(self.messages, list):
            messages = []
            for messages_type_0_item_data in self.messages:
                messages_type_0_item = messages_type_0_item_data.to_dict()
                messages.append(messages_type_0_item)


        else:
            messages = self.messages

        schema = self.schema

        id = self.id

        product_id = self.product_id

        session_id = self.session_id


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "messages": messages,
        })
        if schema is not UNSET:
            field_dict["$schema"] = schema
        if id is not UNSET:
            field_dict["id"] = id
        if product_id is not UNSET:
            field_dict["product_id"] = product_id
        if session_id is not UNSET:
            field_dict["session_id"] = session_id

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        from ..models.aisdk_ui_message import AisdkUIMessage
        d = dict(src_dict)
        def _parse_messages(data: object) -> list[AisdkUIMessage] | None:
            if data is None:
                return data
            try:
                if not isinstance(data, list):
                    raise TypeError()
                messages_type_0 = []
                _messages_type_0 = data
                for messages_type_0_item_data in (_messages_type_0):
                    messages_type_0_item = AisdkUIMessage.from_dict(messages_type_0_item_data)



                    messages_type_0.append(messages_type_0_item)

                return messages_type_0
            except (TypeError, ValueError, AttributeError, KeyError):
                pass
            return cast(list[AisdkUIMessage] | None, data)

        messages = _parse_messages(d.pop("messages"))


        schema = d.pop("$schema", UNSET)

        id = d.pop("id", UNSET)

        product_id = d.pop("product_id", UNSET)

        session_id = d.pop("session_id", UNSET)

        aisdk_request = cls(
            messages=messages,
            schema=schema,
            id=id,
            product_id=product_id,
            session_id=session_id,
        )

        return aisdk_request

