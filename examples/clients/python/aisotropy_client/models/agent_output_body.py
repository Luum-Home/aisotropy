from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset
from typing import cast

if TYPE_CHECKING:
  from ..models.agent_step import AgentStep





T = TypeVar("T", bound="AgentOutputBody")



@_attrs_define
class AgentOutputBody:
    """ 
        Attributes:
            steps (list[AgentStep] | None): Tool decisions and results, in order
            tenant_id (str): Tenant that served the request
            schema (str | Unset): A URL to the JSON Schema for this object.
     """

    steps: list[AgentStep] | None
    tenant_id: str
    schema: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        from ..models.agent_step import AgentStep
        steps: list[dict[str, Any]] | None
        if isinstance(self.steps, list):
            steps = []
            for steps_type_0_item_data in self.steps:
                steps_type_0_item = steps_type_0_item_data.to_dict()
                steps.append(steps_type_0_item)


        else:
            steps = self.steps

        tenant_id = self.tenant_id

        schema = self.schema


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "steps": steps,
            "tenant_id": tenant_id,
        })
        if schema is not UNSET:
            field_dict["$schema"] = schema

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        from ..models.agent_step import AgentStep
        d = dict(src_dict)
        def _parse_steps(data: object) -> list[AgentStep] | None:
            if data is None:
                return data
            try:
                if not isinstance(data, list):
                    raise TypeError()
                steps_type_0 = []
                _steps_type_0 = data
                for steps_type_0_item_data in (_steps_type_0):
                    steps_type_0_item = AgentStep.from_dict(steps_type_0_item_data)



                    steps_type_0.append(steps_type_0_item)

                return steps_type_0
            except (TypeError, ValueError, AttributeError, KeyError):
                pass
            return cast(list[AgentStep] | None, data)

        steps = _parse_steps(d.pop("steps"))


        tenant_id = d.pop("tenant_id")

        schema = d.pop("$schema", UNSET)

        agent_output_body = cls(
            steps=steps,
            tenant_id=tenant_id,
            schema=schema,
        )

        return agent_output_body

