from __future__ import annotations

from collections.abc import Mapping
from typing import Any, TypeVar, BinaryIO, TextIO, TYPE_CHECKING, Generator

from attrs import define as _attrs_define
from attrs import field as _attrs_field

from ..types import UNSET, Unset

from ..types import UNSET, Unset
from typing import cast

if TYPE_CHECKING:
  from ..models.agent_step_args import AgentStepArgs





T = TypeVar("T", bound="AgentStep")



@_attrs_define
class AgentStep:
    """ 
        Attributes:
            tool (str):
            args (AgentStepArgs | Unset):
            result (str | Unset):
     """

    tool: str
    args: AgentStepArgs | Unset = UNSET
    result: str | Unset = UNSET





    def to_dict(self) -> dict[str, Any]:
        from ..models.agent_step_args import AgentStepArgs
        tool = self.tool

        args: dict[str, Any] | Unset = UNSET
        if not isinstance(self.args, Unset):
            args = self.args.to_dict()

        result = self.result


        field_dict: dict[str, Any] = {}

        field_dict.update({
            "tool": tool,
        })
        if args is not UNSET:
            field_dict["args"] = args
        if result is not UNSET:
            field_dict["result"] = result

        return field_dict



    @classmethod
    def from_dict(cls: type[T], src_dict: Mapping[str, Any]) -> T:
        from ..models.agent_step_args import AgentStepArgs
        d = dict(src_dict)
        tool = d.pop("tool")

        _args = d.pop("args", UNSET)
        args: AgentStepArgs | Unset
        if isinstance(_args,  Unset):
            args = UNSET
        else:
            args = AgentStepArgs.from_dict(_args)




        result = d.pop("result", UNSET)

        agent_step = cls(
            tool=tool,
            args=args,
            result=result,
        )

        return agent_step

