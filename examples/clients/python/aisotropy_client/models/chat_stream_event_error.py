from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset
from typing import cast
from typing import Literal, cast

if TYPE_CHECKING:
  from ..models.stream_error_event import StreamErrorEvent





T = TypeVar("T", bound="ChatStreamEventError")



@_attrs_define
class ChatStreamEventError:
    """ 
        Attributes:
            data (StreamErrorEvent):
            event (Literal['error']): The event name.
            id (int | Unset): The event ID.
            retry (int | Unset): The retry time in milliseconds.
     """

    data: StreamErrorEvent
    event: Literal['error']
    id: int | Unset = UNSET
    retry: int | Unset = UNSET
    additional_properties: dict[str, Any] = _attrs_field(init=False, factory=dict)





    def to_dict(self) -> dict[str, Any]:
        from ..models.stream_error_event import StreamErrorEvent
        data = self.data.to_dict()

        event = self.event

        id = self.id

        retry = self.retry


        field_dict: dict[str, Any] = {}
        field_dict.update(self.additional_properties)
        field_dict.update({
            "data": data,
            "event": event,
        })
        if id is not UNSET:
            field_dict["id"] = id
        if retry is not UNSET:
            field_dict["retry"] = retry

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        from ..models.stream_error_event import StreamErrorEvent
        d = dict(src_dict)
        data = StreamErrorEvent.from_dict(d.pop("data"))




        event = cast(Literal['error'] , d.pop("event"))
        if event != 'error':
            raise ValueError(f"event must match const 'error', got '{event}'")

        id = d.pop("id", UNSET)

        retry = d.pop("retry", UNSET)

        chat_stream_event_error = cls(
            data=data,
            event=event,
            id=id,
            retry=retry,
        )


        chat_stream_event_error.additional_properties = d
        return chat_stream_event_error

    @property
    def additional_keys(self) -> list[str]:
        return list(self.additional_properties.keys())

    def __getitem__(self, key: str) -> Any:
        return self.additional_properties[key]

    def __setitem__(self, key: str, value: Any) -> None:
        self.additional_properties[key] = value

    def __delitem__(self, key: str) -> None:
        del self.additional_properties[key]

    def __contains__(self, key: str) -> bool:
        return key in self.additional_properties
