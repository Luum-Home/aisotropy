from http import HTTPStatus
from typing import Any, cast
from urllib.parse import quote

import httpx

from ...client import AuthenticatedClient, Client
from ...types import Response, UNSET
from ... import errors

from ...models.error_model import ErrorModel
from ...types import File, FileTypes
from io import BytesIO
from typing import cast



def _get_kwargs(
    tenant_id: str,
    user_id: str,
    *,
    body: File,

) -> dict[str, Any]:
    headers: dict[str, Any] = {}


    

    

    _kwargs: dict[str, Any] = {
        "method": "post",
        "url": "/v1/tenant/{tenant_id}/user/{user_id}/chat/agui".format(tenant_id=quote(str(tenant_id), safe=""),user_id=quote(str(user_id), safe=""),),
    }

    _kwargs["json"] = body.to_tuple()


    headers["Content-Type"] = "application/json"

    _kwargs["headers"] = headers
    return _kwargs



def _parse_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> Any | ErrorModel:
    if response.status_code == 200:
        response_200 = cast(Any, None)
        return response_200

    response_default = ErrorModel.from_dict(response.json())



    return response_default



def _build_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> Response[Any | ErrorModel]:
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
    body: File,

) -> Response[Any | ErrorModel]:
    """ Chat with the agent via the AG-UI protocol (CopilotKit)

     Accepts an AG-UI RunAgentInput (threadId, runId, OpenAI-shaped messages, plus optional
    tools/context/state which are ignored by this floor adapter) and streams the reply as AG-UI SSE
    events: RUN_STARTED, TEXT_MESSAGE_START, TEXT_MESSAGE_CONTENT per token, TEXT_MESSAGE_END,
    RUN_FINISHED (or RUN_ERROR on failure). tenant_id/user_id come from the path, never the body. It re-
    encodes the SAME streaming pipeline the /chat/stream endpoint runs (StreamCore): identity->ctx
    isolation, quota gate, input/output guardrails, tenant-scoped hydration and cost accounting all
    apply unchanged — only the wire format differs.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (File):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Any | ErrorModel]
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
    body: File,

) -> Any | ErrorModel | None:
    """ Chat with the agent via the AG-UI protocol (CopilotKit)

     Accepts an AG-UI RunAgentInput (threadId, runId, OpenAI-shaped messages, plus optional
    tools/context/state which are ignored by this floor adapter) and streams the reply as AG-UI SSE
    events: RUN_STARTED, TEXT_MESSAGE_START, TEXT_MESSAGE_CONTENT per token, TEXT_MESSAGE_END,
    RUN_FINISHED (or RUN_ERROR on failure). tenant_id/user_id come from the path, never the body. It re-
    encodes the SAME streaming pipeline the /chat/stream endpoint runs (StreamCore): identity->ctx
    isolation, quota gate, input/output guardrails, tenant-scoped hydration and cost accounting all
    apply unchanged — only the wire format differs.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (File):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Any | ErrorModel
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
    body: File,

) -> Response[Any | ErrorModel]:
    """ Chat with the agent via the AG-UI protocol (CopilotKit)

     Accepts an AG-UI RunAgentInput (threadId, runId, OpenAI-shaped messages, plus optional
    tools/context/state which are ignored by this floor adapter) and streams the reply as AG-UI SSE
    events: RUN_STARTED, TEXT_MESSAGE_START, TEXT_MESSAGE_CONTENT per token, TEXT_MESSAGE_END,
    RUN_FINISHED (or RUN_ERROR on failure). tenant_id/user_id come from the path, never the body. It re-
    encodes the SAME streaming pipeline the /chat/stream endpoint runs (StreamCore): identity->ctx
    isolation, quota gate, input/output guardrails, tenant-scoped hydration and cost accounting all
    apply unchanged — only the wire format differs.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (File):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Any | ErrorModel]
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
    body: File,

) -> Any | ErrorModel | None:
    """ Chat with the agent via the AG-UI protocol (CopilotKit)

     Accepts an AG-UI RunAgentInput (threadId, runId, OpenAI-shaped messages, plus optional
    tools/context/state which are ignored by this floor adapter) and streams the reply as AG-UI SSE
    events: RUN_STARTED, TEXT_MESSAGE_START, TEXT_MESSAGE_CONTENT per token, TEXT_MESSAGE_END,
    RUN_FINISHED (or RUN_ERROR on failure). tenant_id/user_id come from the path, never the body. It re-
    encodes the SAME streaming pipeline the /chat/stream endpoint runs (StreamCore): identity->ctx
    isolation, quota gate, input/output guardrails, tenant-scoped hydration and cost accounting all
    apply unchanged — only the wire format differs.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (File):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Any | ErrorModel
     """


    return (await asyncio_detailed(
        tenant_id=tenant_id,
user_id=user_id,
client=client,
body=body,

    )).parsed
