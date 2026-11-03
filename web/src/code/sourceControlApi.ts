import { api } from './api';
import { normalizeStatus, type SourceControlActionRequest, type SourceControlActionResult, type SourceControlDiffDocument, type SourceControlDiffRequest, type SourceControlMetadata, type SourceControlProvider, type SourceControlRepository, type SourceControlRevisionDetail, type SourceControlStatus } from './sourceControlTypes';
const base = (id: string) => `/api/v1/plugins/monaco-editor/bots/${encodeURIComponent(id)}/source-control`;
const repo = (id: string, repository: string) => `${base(id)}/repositories/${encodeURIComponent(repository)}`;
export async function listRepositories(id: string, signal?: AbortSignal): Promise<{repositories: SourceControlRepository[]; providers: SourceControlProvider[]; searchParentRepositories: boolean}> { return api(`${base(id)}/repositories`, { signal }); }
export async function loadStatus(id: string, repository: string): Promise<SourceControlStatus> { return normalizeStatus(await api(`${repo(id,repository)}/status`)); }
export async function loadDiff(id: string, repository: string, target: SourceControlDiffRequest, signal?: AbortSignal): Promise<SourceControlDiffDocument> {
 const query = Object.fromEntries(Object.entries(target).filter(([key,value])=>typeof value==='string' && key!=='scope')) as Record<string,string>;
 const document = await api(`${repo(id,repository)}/diff`,{query,signal});
 return {...document, scope:target.scope || (target.kind==='stash'?'stash':target.groupId==='staged'?'included':'working'),path:document.target.path,oldPath:document.target.oldPath};
}
export async function loadMetadata(id:string,repository:string):Promise<SourceControlMetadata>{return api(`${repo(id,repository)}/metadata`);}
export async function runAction(id:string,repository:string,request:SourceControlActionRequest):Promise<SourceControlActionResult>{return api(`${repo(id,repository)}/actions`,{method:'POST',body:request});}
export async function loadRevisionDetail(id:string,repository:string,ref:string,kind='stash'):Promise<SourceControlRevisionDetail>{return api(`${repo(id,repository)}/detail`,{query:{ref,kind}});}
export async function initializeGitRepository(id:string,rootId:string,path=''){return api(`${base(id)}/initialize`,{method:'POST',body:{rootId,path}});}
export async function cloneGitRepository(id:string,url:string,rootId:string,destination:string){return api(`${base(id)}/clone`,{method:'POST',body:{url,rootId,destination}});}
export async function setParentRepositorySearch(id:string,enabled:boolean){return api(`${base(id)}/settings`,{method:'PUT',body:{searchParentRepositories:enabled}});}
export type P4Settings={roots:Record<string,{server:string;user:string;client:string}>;repositories:Record<string,{tracking:boolean;active:string}>};
export async function loadP4Settings(id:string):Promise<P4Settings>{return (await api(`${base(id)}/settings`)).p4;}
export async function saveP4Settings(id:string,p4:P4Settings):Promise<void>{await api(`${base(id)}/settings`,{method:'PUT',body:{p4}});}
export async function saveP4Credential(id:string,server:string,user:string,secret:string):Promise<void>{await api(`${base(id)}/p4/credential`,{method:'POST',body:{server,user,secret}});}
