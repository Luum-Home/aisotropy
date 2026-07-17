from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset
from typing import cast






T = TypeVar("T", bound="ChatRequest")



@_attrs_define
class ChatRequest:
    """ 
        Attributes:
            product_id (str): Product or module ID
            prompt (str): User message or instruction
            session_id (str): Client-chosen conversation identifier (working-memory session key)
            schema (str | Unset): A URL to the JSON Schema for this object.
            images (list[str] | None | Unset): Optional image URLs or data refs — a presence SIGNAL that routes the turn to
                a vision/premium model target (ImageRule). Image bytes are not yet sent to the model; this wires routing only.
            model_preference (str | Unset): Optional explicit model/gateway-target override (e.g. "cheap"); an explicit
                preference wins over cost-budget routing but quota-pressure degradation still overrides it
            scopes (list[str] | None | Unset): Granted permission scopes gating tool access, e.g. ["write:tasks"]
     """

    product_id: str
    prompt: str
    session_id: str
    schema: str | Unset = UNSET
    images: list[str] | None | Unset = UNSET
    model_preference: str | Unset = UNSET
    scopes: list[str] | None | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        product_id = self.product_id

        prompt = self.prompt

        session_id = self.session_id

        schema = self.schema

        images: list[str] | None | Unset
        if isinstance(self.images, Unset):
            images = UNSET
        elif isinstance(self.images, list):
            images = self.images


        else:
            images = self.images

        model_preference = self.model_preference

        scopes: list[str] | None | Unset
        if isinstance(self.scopes, Unset):
            scopes = UNSET
        elif isinstance(self.scopes, list):
            scopes = self.scopes


        else:
            scopes = self.scopes


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "product_id": product_id,
            "prompt": prompt,
            "session_id": session_id,
        })
        if schema is not UNSET:
            field_dict["$schema"] = schema
        if images is not UNSET:
            field_dict["images"] = images
        if model_preference is not UNSET:
            field_dict["model_preference"] = model_preference
        if scopes is not UNSET:
            field_dict["scopes"] = scopes

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        d = dict(src_dict)
        product_id = d.pop("product_id")

        prompt = d.pop("prompt")

        session_id = d.pop("session_id")

        schema = d.pop("$schema", UNSET)

        def _parse_images(data: object) -> list[str] | None | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            try:
                if not isinstance(data, list):
                    raise TypeError()
                images_type_0 = cast(list[str], data)

                return images_type_0
            except (TypeError, ValueError, AttributeError, KeyError):
                pass
            return cast(list[str] | None | Unset, data)

        images = _parse_images(d.pop("images", UNSET))


        model_preference = d.pop("model_preference", UNSET)

        def _parse_scopes(data: object) -> list[str] | None | Unset:
            if data is None:
                return data
            if isinstance(data, Unset):
                return data
            try:
                if not isinstance(data, list):
                    raise TypeError()
                scopes_type_0 = cast(list[str], data)

                return scopes_type_0
            except (TypeError, ValueError, AttributeError, KeyError):
                pass
            return cast(list[str] | None | Unset, data)

        scopes = _parse_scopes(d.pop("scopes", UNSET))


        chat_request = cls(
            product_id=product_id,
            prompt=prompt,
            session_id=session_id,
            schema=schema,
            images=images,
            model_preference=model_preference,
            scopes=scopes,
        )

        return chat_request

