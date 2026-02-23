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

}

