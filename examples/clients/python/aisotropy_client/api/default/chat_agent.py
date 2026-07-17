from http import HTTPStatus
from typing import Any, cast
from urllib.parse import quote

import httpx

from ...client import AuthenticatedClient, Client
from ...types import Response, UNSET
from ... import errors

from ...models.agent_output_body import AgentOutputBody
from ...models.chat_request import ChatRequest
from ...models.error_model import ErrorModel
from typing import cast



def _get_kwargs(
    tenant_id: str,
    user_id: str,
    *,
    body: ChatRequest,

) -> dict[str, Any]:
    headers: dict[str, Any] = {}


    

    

    _kwargs: dict[str, Any] = {
        "method": "post",
        "url": "/v1/tenant/{tenant_id}/user/{user_id}/chat/agent".format(tenant_id=quote(str(tenant_id), safe=""),user_id=quote(str(user_id), safe=""),),
    }

    _kwargs["json"] = body.to_dict()

    headers["Content-Type"] = "application/json"

    _kwargs["headers"] = headers
    return _kwargs



def _parse_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> AgentOutputBody | ErrorModel:
    if response.status_code == 200:
        response_200 = AgentOutputBody.from_dict(response.json())



        return response_200

    response_default = ErrorModel.from_dict(response.json())



    return response_default



def _build_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> Response[AgentOutputBody | ErrorModel]:
    return Response(
        status_code=HTTPStatus(response.status_code),
        content=response.content,
        headers=response.headers,
        parsed=_parse_response(client=client, response=response),
    )


def sync_detailed(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ChatRequest,

) -> Response[AgentOutputBody | ErrorModel]:
    """ Agentic tool use over structured output (Phase 7)

     Runs a bounded structured-output loop: the model chooses, as JSON, which scope-permitted tool to
    call (or none), the tool is executed by the host, and the result is fed back. Tools the caller's
    scopes do not grant are never injected into the payload nor executable; a failing tool records
    friction for later rewriting.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[AgentOutputBody | ErrorModel]
     """


    kwargs = _get_kwargs(
        tenant_id=tenant_id,
user_id=user_id,
body=body,

    )

    response = client.get_httpx_client().request(
        **kwargs,
    )

    return _build_response(client=client, response=response)

def sync(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ChatRequest,

) -> AgentOutputBody | ErrorModel | None:
    """ Agentic tool use over structured output (Phase 7)

     Runs a bounded structured-output loop: the model chooses, as JSON, which scope-permitted tool to
    call (or none), the tool is executed by the host, and the result is fed back. Tools the caller's
    scopes do not grant are never injected into the payload nor executable; a failing tool records
    friction for later rewriting.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        AgentOutputBody | ErrorModel
     """


    return sync_detailed(
        tenant_id=tenant_id,
user_id=user_id,
client=client,
body=body,

    ).parsed

async def asyncio_detailed(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ChatRequest,

) -> Response[AgentOutputBody | ErrorModel]:
    """ Agentic tool use over structured output (Phase 7)

     Runs a bounded structured-output loop: the model chooses, as JSON, which scope-permitted tool to
    call (or none), the tool is executed by the host, and the result is fed back. Tools the caller's
    scopes do not grant are never injected into the payload nor executable; a failing tool records
    friction for later rewriting.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[AgentOutputBody | ErrorModel]
     """


    kwargs = _get_kwargs(
        tenant_id=tenant_id,
user_id=user_id,
body=body,

    )

    response = await client.get_async_httpx_client().request(
        **kwargs
    )

    return _build_response(client=client, response=response)

async def asyncio(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ChatRequest,

) -> AgentOutputBody | ErrorModel | None:
    """ Agentic tool use over structured output (Phase 7)

     Runs a bounded structured-output loop: the model chooses, as JSON, which scope-permitted tool to
    call (or none), the tool is executed by the host, and the result is fed back. Tools the caller's
    scopes do not grant are never injected into the payload nor executable; a failing tool records
    friction for later rewriting.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        AgentOutputBody | ErrorModel
     """


    return (await asyncio_detailed(
        tenant_id=tenant_id,
user_id=user_id,
client=client,
body=body,

    )).parsed
