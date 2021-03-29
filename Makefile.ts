import * as fs from "fs";
import yargs from "yargs";
import { Logger } from "tslog";
import * as execa from "execa";
import * as hasha from "hasha";
import * as archiver from "archiver";
import * as readline from "readline";
import * as git from "isomorphic-git";
import * as gitHttp from "isomorphic-git/http/node";

// >>> CONFIGURATION
// -----------------------------------------------------------------------------
// Supply input to this task runner via CLI options or environment vars.
//
// see: https://yargs.js.org/
const config = yargs(process.argv.slice(2))
	.option("githubToken", { default: process.env["GITHUB_TOKEN"] })
	.option("versionNo", { default: process.env["VERSION_NO"] ?? "0.0.0" })
	.option("date", {
		default: process.env["DATE"] ?? new Date().toISOString(),
	})
	.option("commitUrl", {
		default:
			process.env["COMMIT_URL"] ??
			"https://github.com/owner/project/commit/hash",
	}).argv;

// >>> LOGGING
// -----------------------------------------------------------------------------
// All output from this task runner will be through this logger.
//
// see: https://tslog.js.org/
const logger = new Logger({
	displayInstanceName: false,
	displayLoggerName: false,
	displayFunctionName: false,
	displayFilePath: "hidden",
});

// >>> UTILS
// -----------------------------------------------------------------------------
// Functions that we use in our tasks.

/**
 * Executes a child process and outputs all stdio through the supplied logger.
 */
async function exe(
	log: Logger,
	args?: readonly string[],
	options?: execa.Options
) {
	const proc = execa(args[0], args.slice(1), options);
	readline
		.createInterface(proc.stdout)
		.on("line", (line: string) => log.info(line));
	readline
		.createInterface(proc.stderr)
		.on("line", (line: string) => log.warn(line));
	return await proc;
}

/**
 * Swallows the "no such file or directory" error when thrown.
 */
async function unlinkIfExists(log: Logger, path: string) {
	try {
		await fs.promises.unlink(path);
		log.info(`deleted ${path}`);
	} catch (e) {
		if (!e.message.includes("no such file or directory")) {
			throw e;
		}
	}
}

// >>> TASKS
// -----------------------------------------------------------------------------
export async function prepareRelease() {
	const log = logger.getChildLogger({ prefix: ["prepareRelease:"] });

	await fs.promises.rmdir("./dist", { recursive: true });
	log.info("rm -rf ./dist");
	await fs.promises.mkdir("./dist/github-downloads", { recursive: true });
	log.info("mkdir -p ./dist/github-downloads");

	await exe(
		log.getChildLogger({ prefix: ["go", "build", "windows"] }),
		[
			"go",
			"build",
			"-ldflags",
			`-X main.versionNo=${config.versionNo} -X main.commitUrl=${config.commitUrl} -X main.date=${config.date}`,
			"-o",
			`./dist/winsudo_amd64`,
			"./cmd/sudo",
		],
		{
			env: {
				CGO_ENABLED: "0",
				GOOS: "windows",
				GOARCH: "amd64",
			},
		}
	);
	log.info(`built ./dist/winsudo_amd64`);

	await Promise.all([
		(async () => {
			const a = archiver("zip", { gzip: true });
			a.append(fs.createReadStream("./README.md"), {
				name: "README.md",
			});
			a.append(fs.createReadStream("./CHANGELOG.md"), {
				name: "CHANGELOG.md",
			});
			a.append(fs.createReadStream("./LICENSE"), { name: "LICENSE" });
			a.append(fs.createReadStream(`./dist/winsudo_amd64`), {
				name: "sudo.exe",
			});
			a.pipe(
				fs.createWriteStream(
					"./dist/github-downloads/winsudo_amd64.zip"
				)
			);
			await a.finalize();
			log.info("packaged ./dist/github-downloads/winsudo_amd64.zip");
		})(),
	]);

	let checksumFile = "";
	for (let file of await fs.promises.readdir("./dist/github-downloads")) {
		const hash = await hasha.fromFile(`./dist/github-downloads/${file}`, {
			algorithm: "sha256",
		});
		checksumFile = `${checksumFile}${hash}  ${file}\n`;
	}
	await fs.promises.writeFile(
		"./dist/github-downloads/sha256_checksums.txt",
		checksumFile,
		"utf8"
	);
	log.info("written ./dist/github-downloads/sha256_checksums.txt");

	await fs.promises.mkdir("./dist/scoop-bucket", { recursive: true });
	log.info("mkdir -p ./dist/scoop-bucket");
	let scoop = await fs.promises.readFile("./scoop.json", "utf8");
	scoop = scoop.replace(/\$\{VERSION\}/g, config.versionNo);
	scoop = scoop.replace(
		/\$\{HASH\}/g,
		await hasha.fromFile("./dist/github-downloads/winsudo_amd64.zip", {
			algorithm: "sha256",
		})
	);
	await fs.promises.writeFile(
		"./dist/scoop-bucket/winsudo.json",
		scoop,
		"utf8"
	);
	log.info("written ./dist/scoop-bucket/winsudo.json");
}

export async function publishRelease() {
	const log = logger.getChildLogger({ prefix: ["publishRelease:"] });

	const scoopLog = log.getChildLogger({ prefix: ["scoop:"] });
	await git.clone({
		fs,
		http: gitHttp,
		url: "https://github.com/brad-jones/scoop-bucket.git",
		dir: "./dist/scoop-bucket/repo",
		onAuth: (url) => ({
			username: "token",
			password: config.githubToken,
		}),
		onMessage: (msg) => {
			scoopLog.info(msg.trim());
		},
	});
	await unlinkIfExists(scoopLog, "./dist/scoop-bucket/repo/winsudo.json");
	await fs.promises.copyFile(
		"./dist/scoop-bucket/winsudo.json",
		"./dist/scoop-bucket/repo/winsudo.json"
	);
	scoopLog.info(
		"copied ./dist/scoop-bucket/winsudo.json => ./dist/scoop-bucket/repo/winsudo.json"
	);
	await git.add({
		fs,
		dir: "./dist/scoop-bucket/repo",
		filepath: "winsudo.json",
	});
	scoopLog.info("git add ./dist/scoop-bucket/repo/winsudo.json");
	await git.commit({
		fs,
		dir: "./dist/scoop-bucket/repo",
		message: `chore(winsudo): release new version ${config.versionNo}`,
		author: {
			name: "semantic-release-bot",
			email: "semantic-release-bot@martynus.net",
		},
	});
	scoopLog.info(
		`git commit -m "chore(winsudo): release new version ${config.versionNo}"`
	);
	scoopLog.info("git push origin master -C ./dist/scoop-bucket/repo");
	await git.push({
		fs,
		http: gitHttp,
		dir: "./dist/scoop-bucket/repo",
		remote: "origin",
		ref: "master",
		onAuth: (url) => ({
			username: "token",
			password: config.githubToken,
		}),
		onMessage: (msg) => {
			scoopLog.info(msg.trim());
		},
	});
}

// >>> ENTRYPOINT
// -----------------------------------------------------------------------------
module.exports[config._[0]]
	.apply(null)
	.then(() => process.exit(0))
	.catch((e) => {
		logger.error(e);
		process.exit(1);
	});
