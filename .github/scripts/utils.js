const fs = require('fs');
const lineReader = require('line-reader');

/**
 * Create a new comment on an issue or pull request.
 * @param {*} github GitHub object reference.
 * @param {*} context GitHub action context.
 * @param {string} content Content of the comment.
 */
async function createComment(github, context, content) {
  await github.issues.createComment({
    owner: context.repo.owner,
    repo: context.repo.repo,
    issue_number: context.issue.number,
    body: content,
  });
}


/**
 * 
 * @param {*} github GitHub object reference.
 * @param {*} context GitHub action context.
 * @param {string} coverageData is colon separated key value pairs of the form component=coverage
 * @param {number} threshold is the minimum coverage required to not issue a warning.
 */
export async function warnOnCertTestCoverage(github, context, coverageData, threshold) {
    const coverages = coverageData.split(':');
    var content = "";
    coverages.forEach(coverage => {
        // skip if empty
        if (coverage.length === 0) { return; }
        const [component, coverageValue] = coverage.split('=');
        coverageValue = parseFloat(coverageValue);
        if (coverageValue < threshold) {
            content += `${component}: ${coverageValue}%\n`;
        }
    })
    if (content.length > 0) {
        const prefix = "Warning, the following components have a coverage below the threshold:\n";
        content = prefix + content;
        await createComment(github, context, content);
    }
}

export async function calculateTotalCoveragePercentage(certTestCovFolder) {
  var totalNumerator = 0;
  var totalDenominator = 0;
  fs.readdir(certTestCovFolder, (_, files) => {
    files.forEach(file => {
      lineReader.eachLine(file,(line,last)=>{
        var regex= /[^\(\}]+(?=\))/g;
        var getRatioInsideBraces= line.match(regex);
        var parts = getRatioInsideBraces[0].split('/');
        totalNumerator += parts[0];
        totalDenominator += parts[1];
      })
    });
  });
  var finalPercentage = totalNumerator / (totalDenominator + 1);
  return finalPercentage;
}