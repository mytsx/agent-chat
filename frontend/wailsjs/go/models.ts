export namespace cli {
	
	export class CLIInfo {
	    type: string;
	    name: string;
	    binary: string;
	    available: boolean;
	    binary_path: string;
	
	    static createFrom(source: any = {}) {
	        return new CLIInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.name = source["name"];
	        this.binary = source["binary"];
	        this.available = source["available"];
	        this.binary_path = source["binary_path"];
	    }
	}

}

export namespace main {
	
	export class OpenTeamResult {
	    agentName: string;
	    cliType: string;
	    slotIndex: number;
	    sessionID: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new OpenTeamResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agentName = source["agentName"];
	        this.cliType = source["cliType"];
	        this.slotIndex = source["slotIndex"];
	        this.sessionID = source["sessionID"];
	        this.error = source["error"];
	    }
	}
	export class RoomSummaryInfo {
	    room: string;
	    text: string;
	    epoch: string;
	    created_at: string;
	    exists: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RoomSummaryInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.room = source["room"];
	        this.text = source["text"];
	        this.epoch = source["epoch"];
	        this.created_at = source["created_at"];
	        this.exists = source["exists"];
	    }
	}
	export class SaveSessionResult {
	    saved: boolean;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new SaveSessionResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.saved = source["saved"];
	        this.count = source["count"];
	    }
	}
	export class SessionInfo {
	    sessionID: string;
	    cliType: string;
	    startUnix: number;
	    lastUnix: number;
	    durationSec: number;
	    messageCount: number;
	    snippet: string;
	    fileMissing: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sessionID = source["sessionID"];
	        this.cliType = source["cliType"];
	        this.startUnix = source["startUnix"];
	        this.lastUnix = source["lastUnix"];
	        this.durationSec = source["durationSec"];
	        this.messageCount = source["messageCount"];
	        this.snippet = source["snippet"];
	        this.fileMissing = source["fileMissing"];
	    }
	}
	export class VoiceStatus {
	    hasKey: boolean;
	    keyHint: string;
	    ffmpegFound: boolean;
	
	    static createFrom(source: any = {}) {
	        return new VoiceStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hasKey = source["hasKey"];
	        this.keyHint = source["keyHint"];
	        this.ffmpegFound = source["ffmpegFound"];
	    }
	}

}

export namespace prompt {
	
	export class Prompt {
	    id: string;
	    name: string;
	    content: string;
	    category: string;
	    tags: string[];
	    variables: string[];
	    created_at: string;
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new Prompt(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.content = source["content"];
	        this.category = source["category"];
	        this.tags = source["tags"];
	        this.variables = source["variables"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}

}

export namespace team {
	
	export class AgentConfig {
	    name: string;
	    role: string;
	    prompt_id: string;
	    work_dir: string;
	    cli_type: string;
	    slot_index: number;
	    use_worktree: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AgentConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.role = source["role"];
	        this.prompt_id = source["prompt_id"];
	        this.work_dir = source["work_dir"];
	        this.cli_type = source["cli_type"];
	        this.slot_index = source["slot_index"];
	        this.use_worktree = source["use_worktree"];
	    }
	}
	export class Team {
	    id: string;
	    name: string;
	    agents: AgentConfig[];
	    grid_layout: string;
	    chat_dir: string;
	    manager_agent: string;
	    custom_prompt: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new Team(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.agents = this.convertValues(source["agents"], AgentConfig);
	        this.grid_layout = source["grid_layout"];
	        this.chat_dir = source["chat_dir"];
	        this.manager_agent = source["manager_agent"];
	        this.custom_prompt = source["custom_prompt"];
	        this.created_at = source["created_at"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace types {
	
	export class Agent {
	    role: string;
	    joined_at: string;
	    last_seen: number;
	
	    static createFrom(source: any = {}) {
	        return new Agent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.role = source["role"];
	        this.joined_at = source["joined_at"];
	        this.last_seen = source["last_seen"];
	    }
	}
	export class Message {
	    id: number;
	    from: string;
	    to: string;
	    original_to?: string;
	    content: string;
	    timestamp: string;
	    type: string;
	    routed_by_manager?: boolean;
	    expects_reply: boolean;
	    priority: string;
	
	    static createFrom(source: any = {}) {
	        return new Message(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.original_to = source["original_to"];
	        this.content = source["content"];
	        this.timestamp = source["timestamp"];
	        this.type = source["type"];
	        this.routed_by_manager = source["routed_by_manager"];
	        this.expects_reply = source["expects_reply"];
	        this.priority = source["priority"];
	    }
	}
	export class RoomSummary {
	    name: string;
	    message_count: number;
	    agents: Record<string, Agent>;
	    historical_agents: string[];
	    last_activity: string;
	    is_default: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RoomSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.message_count = source["message_count"];
	        this.agents = this.convertValues(source["agents"], Agent, true);
	        this.historical_agents = source["historical_agents"];
	        this.last_activity = source["last_activity"];
	        this.is_default = source["is_default"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace usage {
	
	export class Thresholds {
	    warnPercent: number;
	    criticalPercent: number;
	
	    static createFrom(source: any = {}) {
	        return new Thresholds(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.warnPercent = source["warnPercent"];
	        this.criticalPercent = source["criticalPercent"];
	    }
	}

}

